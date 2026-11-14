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
	// lastBranch/lastHead/lastRefresh drive RefreshIfNeeded: refresh when
	// the branch or head moved, at most every prLocalDiffMinInterval, with
	// one trailing refresh coalescing any triggers inside the window.
	// trailing is the armed trailing-edge timer, if any.
	// gen/appliedGen order concurrent refreshes: every Refresh takes the
	// next generation and setState drops results older than the last one
	// applied, so a slow detect can never overwrite a newer header.
	lastBranch  string
	lastHead    string
	lastRefresh time.Time
	trailing    *time.Timer
	gen         uint64
	appliedGen  uint64
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
// prLocalDiffMinInterval is the minimum gap between two local refreshes.
// The diff comes from local git and is cheap; triggers inside the window do
// not refresh immediately but arm one trailing refresh at the window end, so
// the last trigger always lands. The network half (gh/glab identity) has its
// own longer TTL and is untouched by this gate.
const prLocalDiffMinInterval = 2 * time.Second

// prIdentityTTL bounds how long the PR identity (number/url/base for one
// branch) is reused without asking the network again. Diff stats are always
// recomputed from the local repo; only the gh/glab lookup is cached, so the
// header never costs a network round trip per turn or per frame.
const prIdentityTTL = 5 * time.Minute

// prNoPrTTL bounds the negative cache: how long a branch with no open PR
// skips the gh lookup. A PR closed (or opened) right now can linger on — or
// stay off — the header until this lapses; 30 seconds keeps that window short
// while sparing every refresh a network round trip. A tool result from a
// PR/MR-mutating command (gh pr create, git push, ...) bypasses this cache
// entirely via InvalidatePRIdentity, so the normal case — opening the PR
// from inside qg — shows up on the very next refresh.
const prNoPrTTL = 30 * time.Second

type cachedPRIdentity struct {
	branch string
	number string
	url    string
	base   string
	at     time.Time
	// none marks a cached negative: gh worked but no open PR wears the
	// branch. Kept apart from "never cached" by the at timestamp.
	none bool
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
	s.mu.Lock()
	s.gen++
	gen := s.gen
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), prCommandTimeout)
		defer cancel()
		state := s.detect(ctx)
		s.mu.Lock()
		if gen < s.appliedGen {
			// A newer refresh already landed: drop this stale result.
			s.mu.Unlock()
			return
		}
		s.appliedGen = gen
		s.mu.Unlock()
		s.setState(state)
	}()
}

// RefreshIfNeeded re-checks the header state when something observable may
// have changed: a different branch or head refreshes at once; otherwise the
// local diff refreshes at most every prLocalDiffMinInterval, and triggers
// inside the window coalesce into one trailing refresh at the window end —
// the last trigger always lands. The whole gate runs in a service goroutine;
// results flow back as prStateMsg. Never blocks the UI.
// RefreshIfNeeded re-checks the header state when something observable may
// have changed. The decision itself is synchronous and in-memory — no
// subprocess, no goroutine: a burst of N triggers costs no git at all. A
// trigger inside the minimum window either joins an already scheduled
// trailing re-check or arms one; otherwise one background refresh is
// dispatched. Results flow back as prStateMsg. Never blocks the UI.
func (s *PrService) RefreshIfNeeded() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.trailing != nil {
		// A re-check is already scheduled; it covers this trigger.
		s.mu.Unlock()
		return
	}
	if time.Since(s.lastRefresh) < prLocalDiffMinInterval {
		wait := time.Until(s.lastRefresh.Add(prLocalDiffMinInterval))
		if wait < 0 {
			wait = 0
		}
		s.trailing = time.AfterFunc(wait, func() {
			s.mu.Lock()
			s.trailing = nil
			s.mu.Unlock()
			s.RefreshIfNeeded()
		})
		s.mu.Unlock()
		return
	}
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	go s.refreshNow()
}

// refreshNow runs one refresh cycle: read branch and head, stamp them, and
// hand the network-bounded detect to Refresh's goroutine. Runs off the UI
// goroutine; at most one dispatch happens per minimum interval, so tool
// batches cannot multiply git subprocesses.
func (s *PrService) refreshNow() {
	ctx, cancel := context.WithTimeout(context.Background(), prGitTimeout)
	defer cancel()
	branch, head := currentBranchHead(ctx, s.cwd, s.run)
	now := time.Now()
	s.mu.Lock()
	s.lastBranch, s.lastHead, s.lastRefresh = branch, head, now
	s.mu.Unlock()
	s.Refresh()
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

	if state, done := s.detectGitHubPR(ctx, cwd, run); done {
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

// InvalidatePRIdentity drops the cached PR identity (positive or negative),
// forcing the next refresh to consult the network again even inside the TTL.
// Called when a tool result came from a PR/MR-mutating command (gh pr create,
// git push, ...), so a PR the operator just opened shows up on the next
// refresh instead of lingering behind the cache.
func (s *PrService) InvalidatePRIdentity() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cachedIdentity = cachedPRIdentity{}
	s.mu.Unlock()
}

// detectGitHubPR resolves the header for a GitHub repo. done reports whether
// gh itself worked: when true the verdict is final (found, diff-without-PR,
// or definitively nothing) and the caller must not try the next provider.
// A false done means gh failed, so the repo might belong to another forge.
// Open PRs come from `gh pr list --head` with the newest picked
// deterministically: plain `gh pr view` resolves one PR per branch by its own
// rule, which misfires on repos that reuse a head name across many PRs
// (every release-please PR here shares head `dev`).
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
	number, url, base, repoOK := s.openPRForBranch(ctx, cwd, branch, run)
	if !repoOK {
		// gh itself failed: not necessarily a GitHub repo. Let the caller
		// try the next provider instead of hiding the segment outright.
		return PrState{}, false
	}
	gctx, cancel := context.WithTimeout(ctx, prGitTimeout)
	defer cancel()
	if number == "" {
		// GitHub repo, but no open PR for this branch: still show the
		// working diff against the repo default base — only the # segment
		// disappears.
		for _, candidate := range defaultBaseCandidates(gctx, cwd, run) {
			additions, deletions, err := getLocalDiffStats(gctx, cwd, candidate, run)
			if err != nil {
				continue
			}
			return PrState{
				Status:    PrFound,
				Provider:  ProviderGitHub,
				Additions: additions,
				Deletions: deletions,
			}, true
		}
		return PrState{Status: PrNotFound}, true
	}
	state := PrState{
		Status:   PrFound,
		Provider: ProviderGitHub,
		PrURL:    url,
		PrNumber: number,
	}
	state.Additions, state.Deletions, _ = getLocalDiffStats(gctx, cwd, base, run)
	return state, true
}

// defaultBaseCandidates resolves the diff base for a branch without an open
// PR: the repo default (origin/HEAD) first, then origin/main, then
// origin/master. Names are bare (getLocalDiffStats adds the origin/ prefix);
// the caller tries each until one diffs cleanly.
func defaultBaseCandidates(ctx context.Context, cwd string, run prCommandRunner) []string {
	seen := map[string]bool{}
	candidates := []string{}
	if out, err := run(ctx, cwd, "git", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); strings.HasPrefix(ref, "refs/remotes/origin/") {
			candidates = append(candidates, strings.TrimPrefix(ref, "refs/remotes/origin/"))
		}
	}
	candidates = append(candidates, "main", "master")
	unique := candidates[:0]
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		unique = append(unique, candidate)
	}
	return unique
}

// openPRForBranch returns the newest open PR for one head branch, reusing the
// cached identity while its TTL holds so refreshes cost no network call.
// repoOK reports whether gh itself worked (even with no open PR); found is
// implied by a non-empty number. A nil/empty result with repoOK true means
// "GitHub repo, no open PR for this branch" — the caller then shows the
// local diff without a PR number instead of hiding the segment.
func (s *PrService) openPRForBranch(ctx context.Context, cwd, branch string, run prCommandRunner) (number, url, base string, repoOK bool) {
	now := time.Now()
	s.mu.RLock()
	cached := s.cachedIdentity
	s.mu.RUnlock()
	if cached.branch == branch && !cached.at.IsZero() {
		ttl := prIdentityTTL
		if cached.none {
			ttl = prNoPrTTL
		}
		if now.Sub(cached.at) < ttl {
			if cached.none {
				return "", "", "", true
			}
			return cached.number, cached.url, cached.base, true
		}
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
		// gh worked but no open PR wears this head. Cache the negative
		// briefly and keep repoOK true so the caller shows the local diff
		// without a number.
		s.mu.Lock()
		s.cachedIdentity = cachedPRIdentity{branch: branch, at: now, none: true}
		s.mu.Unlock()
		return "", "", "", true
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
	if state.Status != PrFound {
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
	)
	// Without an open PR there is no number to show — only the diff.
	if strings.TrimSpace(state.PrNumber) == "" {
		return segments
	}
	segments = append(segments,
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
