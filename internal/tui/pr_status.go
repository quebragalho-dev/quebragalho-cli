package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
)

type PrStatus string

const (
	PrIdle     PrStatus = "idle"
	PrLoading  PrStatus = "loading"
	PrFound    PrStatus = "found"
	PrNotFound PrStatus = "notfound"
)

type PrProvider string

const (
	ProviderGitHub PrProvider = "github"
	ProviderGitLab PrProvider = "gitlab"
)

type PrState struct {
	Status    PrStatus
	PrURL     string
	PrNumber  string
	Provider  PrProvider
	Additions int
	Deletions int
}

type PrService struct {
	mu        sync.RWMutex
	state     PrState
	listeners []func(PrState)
	cwd       string
	run       prCommandRunner
	// lastBranch/lastHead/lastRefresh drive RefreshIfNeeded: refresh only
	// when the branch or head moved, or the debounce elapsed.
	lastBranch  string
	lastHead    string
	lastRefresh time.Time
	// cachedIdentity reuses one branch's PR number/url/base without
	// hitting the network on every refresh (TTL). Diff stats are always
	// recomputed locally.
	cachedIdentity cachedPRIdentity
}

type prCommandRunner func(context.Context, string, string, ...string) (string, error)

const prRefreshInterval = 60 * time.Second
const prCommandTimeout = 8 * time.Second

// prGitTimeout bounds the local git half of a refresh separately from the
// network half: branch, head and diff stats come from the local repo and must
// never hold the background refresh hostage.
const prGitTimeout = 2 * time.Second

// prRefreshDebounce is the minimum gap between two triggered refreshes. A
// changed branch or head bypasses it; otherwise turn-end triggers coalesce
// here instead of shelling out on every turn.
const prRefreshDebounce = 30 * time.Second

// prIdentityTTL bounds how long the PR identity (number/url/base for one
// branch) is reused without asking the network again. Diff stats are always
// recomputed from the local repo; only the gh/glab lookup is cached, so the
// header never costs a network round trip per turn or per frame.
const prIdentityTTL = 5 * time.Minute

type cachedPRIdentity struct {
	branch string
	number string
	url    string
	base   string
	at     time.Time
}

func NewPrService(cwdValues ...string) *PrService {
	cwd := ""
	if len(cwdValues) > 0 {
		cwd = cwdValues[0]
	}
	if strings.TrimSpace(cwd) == "" {
		if current, err := os.Getwd(); err == nil {
			cwd = current
		}
	}
	return &PrService{
		state: PrState{Status: PrIdle},
		cwd:   cwd,
		run:   defaultPRCommandRunner,
	}
}

func (s *PrService) GetState() PrState {
	if s == nil {
		return PrState{Status: PrIdle}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Branch reports the branch the last background refresh observed: the
// checked-out branch, or the short head sha when detached (mirroring the
// header's git segment). Empty only before the first observation.
func (s *PrService) Branch() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if strings.TrimSpace(s.lastBranch) != "" {
		return strings.TrimSpace(s.lastBranch)
	}
	if len(s.lastHead) >= 7 {
		return s.lastHead[:7]
	}
	return strings.TrimSpace(s.lastHead)
}

func (s *PrService) Subscribe(fn func(PrState)) func() {
	if s == nil || fn == nil {
		return func() {}
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	index := len(s.listeners) - 1
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if index < 0 || index >= len(s.listeners) || s.listeners[index] == nil {
			return
		}
		s.listeners[index] = nil
	}
}

func (s *PrService) Refresh() {
	if s == nil {
		return
	}
	if s.GetState().Status == PrIdle {
		s.setState(PrState{Status: PrLoading})
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), prCommandTimeout)
		defer cancel()
		s.setState(s.detect(ctx))
	}()
}

// RefreshIfNeeded re-checks the header state when something observable may
// have changed: a different branch or head since the last refresh, or a
// debounce interval elapsed with no refresh. The whole gate — including its
// git calls — runs in a service goroutine; results flow back as prStateMsg.
// Never blocks the UI; the render path never shells out.
func (s *PrService) RefreshIfNeeded() {
	if s == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), prGitTimeout)
		defer cancel()
		branch, head := currentBranchHead(ctx, s.cwd, s.run)
		now := time.Now()
		s.mu.Lock()
		changed := branch != s.lastBranch || head != s.lastHead
		due := now.Sub(s.lastRefresh) >= prRefreshDebounce
		if !changed && !due {
			s.mu.Unlock()
			return
		}
		s.lastBranch, s.lastHead, s.lastRefresh = branch, head, now
		s.mu.Unlock()
		s.Refresh()
	}()
}

// currentBranchHead resolves the checked-out branch ("" when detached or on
// any error) and the current head SHA with two fast local-only git calls.
func currentBranchHead(ctx context.Context, cwd string, run prCommandRunner) (string, string) {
	if run == nil {
		run = defaultPRCommandRunner
	}
	branch := ""
	if output, err := run(ctx, cwd, "git", "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		branch = strings.TrimSpace(output)
	}
	head := ""
	if output, err := run(ctx, cwd, "git", "rev-parse", "HEAD"); err == nil {
		head = strings.TrimSpace(output)
	}
	return branch, head
}

func (s *PrService) setState(state PrState) {
	s.mu.Lock()
	s.state = state
	listeners := append([]func(PrState){}, s.listeners...)
	s.mu.Unlock()

	for _, listener := range listeners {
		if listener != nil {
			listener(state)
		}
	}
}

func (s *PrService) detect(ctx context.Context) PrState {
	if s == nil {
		return PrState{Status: PrNotFound}
	}
	run := s.run
	if run == nil {
		run = defaultPRCommandRunner
	}
	cwd := s.cwd
	if strings.TrimSpace(cwd) == "" {
		if current, err := os.Getwd(); err == nil {
			cwd = current
		}
	}

	if state, ok := s.detectGitHubPR(ctx, cwd, run); ok {
		return state
	}
	if state, ok := detectGitLabMR(ctx, cwd, run); ok {
		return state
	}
	return PrState{Status: PrNotFound}
}

func WatchPRStateContext(ctx context.Context, service *PrService, onChange func(PrState)) func() {
	if service == nil || onChange == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	onChange(service.GetState())
	unsubscribe := service.Subscribe(onChange)
	service.RefreshIfNeeded()

	go func() {
		ticker := time.NewTicker(prRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				service.RefreshIfNeeded()
			}
		}
	}()

	return func() {
		cancel()
		unsubscribe()
	}
}

type githubPRView struct {
	URL         string          `json:"url"`
	Number      json.RawMessage `json:"number"`
	BaseRefName string          `json:"baseRefName"`
}

type gitlabMRView struct {
	WebURL       string          `json:"web_url"`
	IID          json.RawMessage `json:"iid"`
	TargetBranch string          `json:"target_branch"`
}

// detectGitHubPR resolves the open PR for the currently checked-out branch.
// `gh pr view` without arguments picks one PR per branch by its own rule,
// which misfires on repos that reuse a head branch name across many PRs
// (every release-please PR here shares head `dev`). Listing the open PRs for
// the exact head and showing the newest is deterministic: the header never
// shows another branch's PR, and never an older duplicate.
// The header segment always matches the PR state it was delivered with:
// both come from one background refresh cycle, keyed by the same observed
// branch — the render path never re-reads files or shells out to stay
// coherent with the diff numbers beside it.
func (s *PrService) detectGitHubPR(ctx context.Context, cwd string, run prCommandRunner) (PrState, bool) {
	s.mu.RLock()
	branch := strings.TrimSpace(s.lastBranch)
	s.mu.RUnlock()
	if branch == "" {
		return PrState{}, false
	}
	number, url, base, ok := s.openPRForBranch(ctx, cwd, branch, run)
	if !ok {
		return PrState{}, false
	}
	state := PrState{
		Status:   PrFound,
		Provider: ProviderGitHub,
		PrURL:    url,
		PrNumber: number,
	}
	gctx, cancel := context.WithTimeout(ctx, prGitTimeout)
	defer cancel()
	state.Additions, state.Deletions, _ = getLocalDiffStats(gctx, cwd, base, run)
	return state, true
}

// openPRForBranch returns the newest open PR for one head branch, reusing the
// cached identity while its TTL holds so refreshes cost no network call.
func (s *PrService) openPRForBranch(ctx context.Context, cwd, branch string, run prCommandRunner) (number, url, base string, ok bool) {
	now := time.Now()
	s.mu.RLock()
	cached := s.cachedIdentity
	s.mu.RUnlock()
	if cached.branch == branch && cached.number != "" && now.Sub(cached.at) < prIdentityTTL {
		return cached.number, cached.url, cached.base, true
	}
	output, err := run(ctx, cwd, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number,url,baseRefName")
	if err != nil {
		return "", "", "", false
	}
	var views []githubPRView
	if err := json.Unmarshal([]byte(output), &views); err != nil {
		return "", "", "", false
	}
	best := -1
	bestNum := -1
	for i, view := range views {
		// Numeric compare: PR numbers are decimal ids, and "100" sorts before
		// "36" lexicographically. The newest open PR wins deterministically.
		n, err := strconv.Atoi(strings.TrimSpace(jsonScalarString(view.Number)))
		if err != nil {
			continue
		}
		if n > bestNum {
			bestNum, best = n, i
		}
	}
	if best < 0 {
		return "", "", "", false
	}
	number = strings.TrimSpace(jsonScalarString(views[best].Number))
	url = strings.TrimSpace(views[best].URL)
	base = strings.TrimSpace(views[best].BaseRefName)
	s.mu.Lock()
	s.cachedIdentity = cachedPRIdentity{branch: branch, number: number, url: url, base: base, at: now}
	s.mu.Unlock()
	return number, url, base, true
}

func detectGitLabMR(ctx context.Context, cwd string, run prCommandRunner) (PrState, bool) {
	output, err := run(ctx, cwd, "glab", "mr", "view", "--json", "web_url,iid,target_branch")
	if err != nil {
		return PrState{}, false
	}
	var view gitlabMRView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return PrState{}, false
	}
	state := PrState{
		Status:   PrFound,
		Provider: ProviderGitLab,
		PrURL:    strings.TrimSpace(view.WebURL),
		PrNumber: jsonScalarString(view.IID),
	}
	if state.PrNumber == "" {
		return PrState{}, false
	}
	state.Additions, state.Deletions, _ = getLocalDiffStats(ctx, cwd, view.TargetBranch, run)
	return state, true
}

func getLocalDiffStats(ctx context.Context, cwd string, baseBranch string, run prCommandRunner) (int, int, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		return 0, 0, nil
	}
	originBase := "origin/" + baseBranch
	mergeBase, err := run(ctx, cwd, "git", "merge-base", originBase, "HEAD")
	if err != nil {
		mergeBase = originBase
	}
	output, err := run(ctx, cwd, "git", "diff", "--numstat", strings.TrimSpace(mergeBase))
	if err != nil {
		return 0, 0, err
	}
	additions, deletions := parseGitNumStat(output)
	return additions, deletions, nil
}

func parseGitNumStat(output string) (int, int) {
	additions := 0
	deletions := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		additions += parseNumStatValue(fields[0])
		deletions += parseNumStatValue(fields[1])
	}
	return additions, deletions
}

func parseNumStatValue(value string) int {
	count, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return count
}

func jsonScalarString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.Itoa(number)
	}
	return ""
}

func defaultPRCommandRunner(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	if strings.TrimSpace(cwd) != "" {
		command.Dir = cwd
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, detail)
	}
	return stdout.String(), nil
}

type Color string

const (
	ColorDefault Color = "default"
	ColorAccent  Color = "accent"
	ColorGreen   Color = "green"
	ColorRed     Color = "red"
)

type PrSegment struct {
	Text  string
	Color Color
	Bold  bool
	URL   string
}

func BuildPRSegments(state PrState, useNerdFont bool) []PrSegment {
	if state.Status != PrFound || strings.TrimSpace(state.PrNumber) == "" {
		return nil
	}
	url := strings.TrimSpace(state.PrURL)
	segments := []PrSegment{}
	if useNerdFont {
		segments = append(segments, PrSegment{Text: " ", Color: ColorAccent, Bold: true, URL: url})
	}
	segments = append(segments,
		PrSegment{Text: "+" + strconv.Itoa(maxInt(0, state.Additions)), Color: ColorGreen, Bold: true, URL: url},
		PrSegment{Text: " ", Color: ColorDefault, URL: url},
		PrSegment{Text: "-" + strconv.Itoa(maxInt(0, state.Deletions)), Color: ColorRed, Bold: true, URL: url},
		PrSegment{Text: " ", Color: ColorDefault, URL: url},
		PrSegment{Text: "#" + strings.TrimSpace(state.PrNumber), Color: ColorAccent, Bold: true, URL: url},
	)
	return segments
}

func renderPRSegments(segments []PrSegment) string {
	var builder strings.Builder
	for _, segment := range segments {
		text := renderPRSegment(segment)
		if segment.URL != "" {
			text = hyperlink(segment.URL, text)
		}
		builder.WriteString(text)
	}
	return builder.String()
}

func renderPRSegment(segment PrSegment) string {
	style := lipgloss.NewStyle()
	switch segment.Color {
	case ColorAccent:
		style = qgTheme.accent
	case ColorGreen:
		style = qgTheme.gitAdd
	case ColorRed:
		style = qgTheme.gitDel
	case ColorDefault:
		style = qgTheme.muted
	}
	if segment.Bold {
		style = style.Bold(true)
	}
	return style.Render(segment.Text)
}
