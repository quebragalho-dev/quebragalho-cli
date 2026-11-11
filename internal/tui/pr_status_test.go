package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestPrServiceDetectsGitHubPRAndDiffStats(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/1337","number":1337,"baseRefName":"main"}]`,
		},
		"git merge-base origin/main HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "42\t7\tinternal/tui/view.go\n",
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "feat/duo"

	state := service.detect(context.Background())

	if state.Status != PrFound || state.Provider != ProviderGitHub || state.PrNumber != "1337" {
		t.Fatalf("unexpected PR state: %#v", state)
	}
	if state.PrURL != "https://github.com/org/repo/pull/1337" {
		t.Fatalf("PrURL = %q", state.PrURL)
	}
	if state.Additions != 42 || state.Deletions != 7 {
		t.Fatalf("diff stats = +%d -%d, want +42 -7", state.Additions, state.Deletions)
	}
	if runner.called("glab mr view --json web_url,iid,target_branch") {
		t.Fatal("GitLab fallback should not run when GitHub PR is found")
	}
}

func TestPrServiceFallsBackToGitLabMR(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			err: errors.New("no pull request found"),
		},
		"glab mr view --json web_url,iid,target_branch": {
			output: `{"web_url":"https://gitlab.com/org/repo/-/merge_requests/55","iid":55,"target_branch":"develop"}`,
		},
		"git merge-base origin/develop HEAD": {
			output: "def456\n",
		},
		"git diff --numstat def456": {
			output: "4\t5\ta.go\n5\t6\tb.go\n",
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "feat/duo"

	state := service.detect(context.Background())

	if state.Status != PrFound || state.Provider != ProviderGitLab || state.PrNumber != "55" {
		t.Fatalf("unexpected MR state: %#v", state)
	}
	if state.Additions != 9 || state.Deletions != 11 {
		t.Fatalf("diff stats = +%d -%d, want +9 -11", state.Additions, state.Deletions)
	}
}

func TestGetLocalDiffStatsFallsBackToOriginBase(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git merge-base origin/main HEAD": {
			err: errors.New("unknown revision"),
		},
		"git diff --numstat origin/main": {
			output: "3\t0\tREADME.md\n",
		},
	}}

	additions, deletions, err := getLocalDiffStats(context.Background(), "/workspace/qg", "main", runner.Run)
	if err != nil {
		t.Fatalf("getLocalDiffStats returned error: %v", err)
	}
	if additions != 3 || deletions != 0 {
		t.Fatalf("diff stats = +%d -%d, want +3 -0", additions, deletions)
	}
}

func TestParseGitNumStatSumsLocaleIndependentColumns(t *testing.T) {
	additions, deletions := parseGitNumStat("10\t2\tfile one.go\n-\t-\tassets/logo.png\n3\t4\tdir/file.go\n")
	if additions != 13 || deletions != 6 {
		t.Fatalf("diff stats = +%d -%d, want +13 -6", additions, deletions)
	}
}

func TestBuildPRSegments(t *testing.T) {
	state := PrState{
		Status:    PrFound,
		PrURL:     "https://github.com/org/repo/pull/1337",
		PrNumber:  "1337",
		Additions: 42,
		Deletions: 7,
	}

	segments := BuildPRSegments(state, false)
	gotText := ""
	for _, segment := range segments {
		gotText += segment.Text
		if segment.URL != state.PrURL {
			t.Fatalf("segment %#v should carry PR hyperlink", segment)
		}
	}
	if gotText != "+42 -7 #1337" {
		t.Fatalf("segments text = %q", gotText)
	}
}

func TestTitleBarShowsPRDiffStatsBesideBranch(t *testing.T) {
	m := limeTestModel()
	m.cwd = "/workspace/qg"
	m.gitBranch = "feat/title-polish"
	m.prState = PrState{
		Status:    PrFound,
		PrURL:     "https://github.com/org/repo/pull/219",
		PrNumber:  "219",
		Additions: 144,
		Deletions: 32,
	}

	rendered := m.titleBar(120)
	plain := plainRender(t, rendered)
	for _, want := range []string{" feat/title-polish", "+144", "-32", "#219"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("title bar = %q, missing %q", plain, want)
		}
	}
	if !strings.Contains(rendered, "\x1b]8;;"+m.prState.PrURL+"\x1b\\") {
		t.Fatalf("title bar should include OSC 8 PR hyperlink: %q", rendered)
	}

	footer := plainRender(t, m.footerView(120))
	for _, notWant := range []string{"+144", "-32", "#219"} {
		if strings.Contains(footer, notWant) {
			t.Fatalf("footer = %q, should not contain title-bar PR stat %q", footer, notWant)
		}
	}
}

type fakePRResult struct {
	output string
	err    error
}

type fakePRRunner struct {
	mu      sync.Mutex
	results map[string]fakePRResult
	calls   []string
}

func (runner *fakePRRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	runner.mu.Lock()
	runner.calls = append(runner.calls, call)
	runner.mu.Unlock()
	result, ok := runner.results[call]
	if !ok {
		return "", errors.New("unexpected command: " + call)
	}
	return result.output, result.err
}

func (runner *fakePRRunner) called(call string) bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, actual := range runner.calls {
		if actual == call {
			return true
		}
	}
	return false
}

func (runner *fakePRRunner) anyCallWithPrefix(prefix string) bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, actual := range runner.calls {
		if strings.HasPrefix(actual, prefix) {
			return true
		}
	}
	return false
}

func (runner *fakePRRunner) callsSnapshot() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]string{}, runner.calls...)
}

// The repo reuses head `dev` for every release PR (#13-#100): `gh pr view`
// resolves one PR per branch by its own rule, so the header can show a stale
// number. The service must list the open PRs for the exact head and show the
// newest deterministically.
func TestPrServicePicksNewestOpenPrForBranch(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "dev\n",
		},
		"gh pr list --head dev --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/36","number":36,"baseRefName":"main"},{"url":"https://github.com/org/repo/pull/100","number":100,"baseRefName":"main"}]`,
		},
		"git merge-base origin/main HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "42\t7\tinternal/tui/view.go\n",
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "dev"

	state := service.detect(context.Background())

	if state.Status != PrFound || state.PrNumber != "100" {
		t.Fatalf("must show the newest open PR for the branch, got %#v", state)
	}
	if state.PrURL != "https://github.com/org/repo/pull/100" {
		t.Fatalf("PrURL = %q, want the newest PR", state.PrURL)
	}
	if state.Additions != 42 || state.Deletions != 7 {
		t.Fatalf("diff stats = +%d -%d, want +42 -7", state.Additions, state.Deletions)
	}
}

// No open PR for the branch: the header shows nothing, it never falls back
// to another branch's PR.
func TestPrServiceHidesWhenBranchHasNoOpenPr(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/lonely\n",
		},
		"gh pr list --head feat/lonely --state open --json number,url,baseRefName": {
			output: `[]`,
		},
		"glab mr view --json web_url,iid,target_branch": {
			err: errors.New("no mr found"),
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "feat/lonely"

	state := service.detect(context.Background())

	if state.Status != PrNotFound {
		t.Fatalf("must hide the PR segment without an open PR, got %#v", state)
	}
	if runner.called("git diff --numstat") {
		t.Fatal("no diff stats should run without a PR")
	}
}

// Finishing a turn re-checks the header state without ever blocking the UI:
// file changes from the run's tools must surface without waiting for the
// minute ticker, and even if every git/gh call hangs, Update returns at once
// while the refresh lands afterwards through the service goroutine.
func TestTurnEndRefreshesPrState(t *testing.T) {
	release := make(chan struct{})
	runner := &blockingPRRunner{release: release, results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/97","number":97,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "10\t2\tchanged.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run

	m := newQueuedMessageTestModel(t)
	m.prService = service
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.width = 96

	start := time.Now()
	updated, cmd := m.Update(agentResponseMsg{runID: 7})
	m = updated.(model)
	if took := time.Since(start); took >= 500*time.Millisecond {
		t.Fatalf("turn-end Update blocked %v waiting on the PR refresh", took)
	}
	close(release)
	_ = execCmd(cmd)

	deadline := time.Now().Add(5 * time.Second)
	for service.GetState().PrNumber != "97" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "97" || got.Additions != 10 {
		t.Fatalf("refreshed state = %#v, want PR 97 with +10 -2", got)
	}
}

// blockingPRRunner records every command like fakePRRunner but holds each
// call until released, proving callers never wait on it synchronously.
type blockingPRRunner struct {
	mu      sync.Mutex
	calls   []string
	results map[string]fakePRResult
	release chan struct{}
}

func (runner *blockingPRRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	runner.mu.Lock()
	runner.calls = append(runner.calls, call)
	runner.mu.Unlock()
	select {
	case <-runner.release:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	result, ok := runner.results[call]
	if !ok {
		return "", errors.New("unexpected command: " + call)
	}
	return result.output, result.err
}

// Rendering the title bar never shells out: all git/gh work happens in the
// background service, never in the render path.
func TestTitleBarRendersWithoutSubprocess(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run

	m := limeTestModel()
	m.cwd = "/workspace/qg"
	m.gitBranch = "feat/title-polish"
	m.prService = service
	m.prState = PrState{
		Status:    PrFound,
		PrURL:     "https://github.com/org/repo/pull/219",
		PrNumber:  "219",
		Additions: 144,
		Deletions: 32,
	}

	_ = plainRender(t, m.titleBar(120))
	_ = plainRender(t, m.titleBar(120))
	if len(runner.calls) != 0 {
		t.Fatalf("title bar render must not shell out, ran: %v", runner.calls)
	}
}

// The refresh gate: a fresh branch state performs no subprocess at once; a
// changed branch (or head) triggers a background refresh. Triggers inside
// the local interval defer to one trailing refresh (tested separately).
func TestPrServiceRefreshIfNeededDefersFreshState(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/duo"
	service.lastHead = "abc123"
	service.lastRefresh = time.Now()

	service.RefreshIfNeeded()
	time.Sleep(200 * time.Millisecond)
	for _, prefix := range []string{"gh ", "git merge-base", "git diff"} {
		if runner.anyCallWithPrefix(prefix) {
			t.Fatalf("fresh gate must not shell out to git/gh (prefix %q)", prefix)
		}
	}
}

func TestPrServiceRefreshIfNeededFiresOnBranchChange(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/new\n",
		},
		"git rev-parse HEAD": {
			output: "def456\n",
		},
		"gh pr list --head feat/new --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/101","number":101,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "def456\n",
		},
		"git diff --numstat def456": {
			output: "1\t1\tnew.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/old"
	service.lastHead = "abc123"
	service.lastRefresh = time.Now()

	service.RefreshIfNeeded()
	deadline := time.Now().Add(3 * time.Second)
	for service.GetState().PrNumber != "101" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "101" || got.Additions != 1 {
		t.Fatalf("refreshed state = %#v, want PR 101 with +1 -1", got)
	}
}

// A background refresh (ticker or trigger, no turn running) delivers the
// current branch too: an external branch switch shows up at the next refresh
// without any turn, file read, or subprocess on the UI path.
func TestBackgroundRefreshDeliversBranchWithoutTurn(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/switched-elsewhere\n",
		},
		"git rev-parse HEAD": {
			output: "def456\n",
		},
		"gh pr list --head feat/switched-elsewhere --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/101","number":101,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "def456\n",
		},
		"git diff --numstat def456": {
			output: "3\t1\tnew.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/old"
	service.lastHead = "abc123"
	service.lastRefresh = time.Now().Add(-time.Hour)

	service.RefreshIfNeeded()
	deadline := time.Now().Add(3 * time.Second)
	for service.Branch() != "feat/switched-elsewhere" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.Branch(); got != "feat/switched-elsewhere" {
		t.Fatalf("background refresh must deliver the switched branch, got %q", got)
	}
	for service.GetState().PrNumber != "101" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "101" {
		t.Fatalf("background refresh must deliver the new branch PR, got %#v", got)
	}
}

// The model takes the branch from the incoming message — never a file read
// or subprocess on the Update path.
func TestPrStateMessageCarriesBranch(t *testing.T) {
	m := limeTestModel()
	m.cwd = t.TempDir()
	m.gitBranch = "feat/old"
	state := PrState{Status: PrFound, PrNumber: "101", Additions: 3, Deletions: 1}

	updated, _ := m.Update(prStateMsg{state: state, branch: "feat/switched-elsewhere"})
	next := updated.(model)
	if next.gitBranch != "feat/switched-elsewhere" {
		t.Fatalf("gitBranch = %q, want the branch delivered by the message", next.gitBranch)
	}
	if next.prState.PrNumber != "101" {
		t.Fatalf("prState = %#v, want the delivered PR state", next.prState)
	}

	// An empty branch (service not yet observed) keeps the current segment.
	updated, _ = next.Update(prStateMsg{state: state})
	if got := updated.(model).gitBranch; got != "feat/switched-elsewhere" {
		t.Fatalf("empty branch must keep the segment, got %q", got)
	}
}

// A finished tool may have rewritten files the diff counts: the result
// landing in the transcript re-checks the header without waiting for the
// turn to end. Coalesced by the service gate — rapid tools share one refresh.
func TestToolResultRefreshesPrState(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/97","number":97,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "10\t2\tchanged.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/old"
	service.lastHead = "abc000"
	service.lastRefresh = time.Now()

	m := newQueuedMessageTestModel(t)
	m.prService = service
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.width = 96

	updated, cmd := m.Update(agentRowMsg{
		runID: 7,
		row:   transcriptRow{kind: rowToolResult, id: "c1", tool: "exec_command", status: tools.StatusOK, runID: 7},
	})
	_ = updated.(model)
	_ = execCmd(cmd)

	deadline := time.Now().Add(3 * time.Second)
	for !runner.called("gh pr list --head feat/duo --state open --json number,url,baseRefName") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !runner.called("gh pr list --head feat/duo --state open --json number,url,baseRefName") {
		t.Fatal("a finished tool result must re-check the PR header state")
	}
}

// Triggers inside the minimum local interval must not be dropped: the last
// one always produces a refresh at the window end (trailing edge).
func TestRefreshIfNeededCoalescesTrailingEdge(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/97","number":97,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "10\t2\tchanged.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/duo"
	service.lastHead = "abc123"
	service.lastRefresh = time.Now()

	service.RefreshIfNeeded()
	service.RefreshIfNeeded()

	deadline := time.Now().Add(6 * time.Second)
	for service.GetState().PrNumber != "97" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "97" || got.Additions != 10 {
		t.Fatalf("trailing trigger must refresh, got %#v", got)
	}
}

// Without an open PR for the branch the header still shows the working diff,
// against the repo default base — only the # segment disappears.
func TestPrStateWithoutOpenPrShowsDiffAgainstDefaultBase(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "dev\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head dev --state open --json number,url,baseRefName": {
			output: `[]`,
		},
		"git symbolic-ref --quiet refs/remotes/origin/HEAD": {
			output: "refs/remotes/origin/main\n",
		},
		"git merge-base origin/main HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "8\t3\twork.go\n",
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "dev"

	state := service.detect(context.Background())

	if state.Status != PrFound || state.PrNumber != "" {
		t.Fatalf("must show the diff without a PR number, got %#v", state)
	}
	if state.Additions != 8 || state.Deletions != 3 {
		t.Fatalf("diff stats = +%d -%d, want +8 -3", state.Additions, state.Deletions)
	}
	segments := BuildPRSegments(state, false)
	text := ""
	for _, segment := range segments {
		text += segment.Text
	}
	if text != "+8 -3" {
		t.Fatalf("segments = %q, want diff without a # segment", text)
	}
}

// Default-base fallback chain: origin/HEAD missing resolves through
// origin/main before origin/master.
func TestPrStateWithoutOpenPrFallsBackToOriginMain(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "dev\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head dev --state open --json number,url,baseRefName": {
			output: `[]`,
		},
		"git symbolic-ref --quiet refs/remotes/origin/HEAD": {
			err: errors.New("unknown revision"),
		},
		"git merge-base origin/main HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "1\t1\tfix.go\n",
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "dev"

	state := service.detect(context.Background())

	if state.Status != PrFound || state.PrNumber != "" {
		t.Fatalf("must show the diff without a PR number, got %#v", state)
	}
	if state.Additions != 1 || state.Deletions != 1 {
		t.Fatalf("diff stats = +%d -%d, want +1 -1", state.Additions, state.Deletions)
	}
}

// Negative cache: without an open PR, repeated refreshes must not hit the
// network every time. A freshly closed PR can linger on the header until the
// short TTL lapses — documented on prNoPrTTL.
func TestPrServiceCachesMissingPrBriefly(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "dev\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head dev --state open --json number,url,baseRefName": {
			output: `[]`,
		},
		"git symbolic-ref --quiet refs/remotes/origin/HEAD": {
			err: errors.New("unknown revision"),
		},
		"git merge-base origin/main HEAD": {
			err: errors.New("unknown revision"),
		},
		"git merge-base origin/master HEAD": {
			err: errors.New("unknown revision"),
		},
	}}
	service := NewPrService("/workspace/qg")
	service.run = runner.Run
	service.lastBranch = "dev"

	_ = service.detect(context.Background())
	_ = service.detect(context.Background())

	ghCalls := 0
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "gh pr list") {
			ghCalls++
		}
	}
	if ghCalls != 1 {
		t.Fatalf("missing-PR branch must hit gh once per TTL, hit %d times: %v", ghCalls, runner.calls)
	}
}

// A slow refresh must not overwrite a newer one: each Refresh takes a
// generation and setState drops results older than the last applied one.
func TestStaleRefreshResultIsDropped(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	ghCalls := 0
	run := func(ctx context.Context, cwd string, name string, args ...string) (string, error) {
		call := strings.TrimSpace(name + " " + strings.Join(args, " "))
		if strings.HasPrefix(call, "gh ") {
			mu.Lock()
			ghCalls++
			n := ghCalls
			mu.Unlock()
			if n == 1 {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
				return `[{"url":"https://github.com/org/repo/pull/100","number":100,"baseRefName":"dev"}]`, nil
			}
			return `[{"url":"https://github.com/org/repo/pull/101","number":101,"baseRefName":"dev"}]`, nil
		}
		switch call {
		case "git symbolic-ref --quiet --short HEAD":
			return "dev\n", nil
		case "git rev-parse HEAD":
			return "abc123\n", nil
		case "git merge-base origin/dev HEAD":
			return "abc123\n", nil
		case "git diff --numstat abc123":
			return "1\t1\ta.go\n", nil
		}
		return "", errors.New("unexpected command: " + call)
	}
	service := NewPrService(t.TempDir())
	service.run = run
	service.lastBranch = "dev"

	service.Refresh() // gen1: gh blocks until released
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first refresh never reached gh")
	}
	service.Refresh() // gen2: completes fast with PR 101

	deadline := time.Now().Add(3 * time.Second)
	for service.GetState().PrNumber != "101" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "101" {
		t.Fatalf("newer refresh must win, got %#v", got)
	}
	close(release) // gen1 finishes late with PR 100
	time.Sleep(300 * time.Millisecond)
	if got := service.GetState(); got.PrNumber != "101" {
		t.Fatalf("stale refresh must not overwrite newer state, got %#v", got)
	}
}

// A burst of triggers coalesces: gate git probing runs at most once and a
// single trailing refresh lands — a 30-tool batch must not shell out 60
// times.
func TestRefreshBurstRunsOneGateProbe(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
		},
		"git rev-parse HEAD": {
			output: "abc123\n",
		},
		"gh pr list --head feat/duo --state open --json number,url,baseRefName": {
			output: `[{"url":"https://github.com/org/repo/pull/101","number":101,"baseRefName":"dev"}]`,
		},
		"git merge-base origin/dev HEAD": {
			output: "abc123\n",
		},
		"git diff --numstat abc123": {
			output: "10\t2\tchanged.go\n",
		},
	}}
	service := NewPrService(t.TempDir())
	service.run = runner.Run
	service.lastBranch = "feat/duo"
	service.lastHead = "abc123"
	service.lastRefresh = time.Now()

	for i := 0; i < 10; i++ {
		service.RefreshIfNeeded()
	}
	deadline := time.Now().Add(6 * time.Second)
	for service.GetState().PrNumber != "101" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "101" {
		t.Fatalf("burst must land one refresh, got %#v", got)
	}
	symrefs, revparses := 0, 0
	runner.mu.Lock()
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "git symbolic-ref") {
			symrefs++
		}
		if strings.HasPrefix(call, "git rev-parse") {
			revparses++
		}
	}
	runner.mu.Unlock()
	if symrefs > 1 || revparses > 1 {
		snapshot := append([]string{}, runner.callsSnapshot()...)
		t.Fatalf("burst must probe the gate at most once, got %d symbolic-ref and %d rev-parse calls: %v", symrefs, revparses, snapshot)
	}
}
