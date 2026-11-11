package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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

	state := service.detect(context.Background())

	if state.Status != PrNotFound {
		t.Fatalf("must hide the PR segment without an open PR, got %#v", state)
	}
	if runner.called("git diff --numstat") {
		t.Fatal("no diff stats should run without a PR")
	}
}

// Finishing a turn re-checks the header: file changes from the run's tools
// must surface without waiting for the minute ticker.
func TestTurnEndRefreshesPrState(t *testing.T) {
	runner := &fakePRRunner{results: map[string]fakePRResult{
		"git symbolic-ref --quiet --short HEAD": {
			output: "feat/duo\n",
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

	updated, _ := m.Update(agentResponseMsg{runID: 7})
	_ = updated.(model)

	deadline := time.Now().Add(3 * time.Second)
	for !runner.called("gh pr list --head feat/duo --state open --json number,url,baseRefName") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !runner.called("gh pr list --head feat/duo --state open --json number,url,baseRefName") {
		t.Fatal("finishing a turn must re-check the PR header state")
	}
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

// The refresh gate: a fresh branch state performs no subprocess at all; a
// changed branch (or head) triggers a background refresh.
func TestPrServiceRefreshIfNeededSkipsFreshState(t *testing.T) {
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

	if service.RefreshIfNeeded() {
		t.Fatal("fresh branch state must not trigger a refresh")
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "gh ") || strings.HasPrefix(call, "git merge-base") || strings.HasPrefix(call, "git diff") {
			t.Fatalf("fresh gate must not shell out to git/gh, ran: %v", runner.calls)
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

	if !service.RefreshIfNeeded() {
		t.Fatal("changed branch must trigger a refresh")
	}
	deadline := time.Now().Add(3 * time.Second)
	for service.GetState().PrNumber != "101" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := service.GetState(); got.PrNumber != "101" || got.Additions != 1 {
		t.Fatalf("refreshed state = %#v, want PR 101 with +1 -1", got)
	}
}
