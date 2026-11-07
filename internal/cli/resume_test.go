package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

func TestSplitLeadingResumeFlags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCont bool
		wantID   string
		wantRes  bool
		wantRID  string
		wantRest []string
		wantErr  string
	}{
		{name: "continue bare", args: []string{"--continue"}, wantCont: true},
		{name: "short c bare", args: []string{"-c"}, wantCont: true},
		{name: "continue id", args: []string{"--continue", "abc123"}, wantCont: true, wantID: "abc123"},
		{name: "short c id", args: []string{"-c", "abc123"}, wantCont: true, wantID: "abc123"},
		{name: "continue eq", args: []string{"--continue=abc123"}, wantCont: true, wantID: "abc123"},
		{name: "resume bare", args: []string{"--resume"}, wantRes: true},
		{name: "resume id", args: []string{"--resume", "abc123"}, wantRes: true, wantRID: "abc123"},
		{name: "resume eq", args: []string{"--resume=abc123"}, wantRes: true, wantRID: "abc123"},
		{name: "both rejected", args: []string{"--continue", "--resume"}, wantErr: "Use either --continue or --resume"},
		{name: "subcommand not id", args: []string{"--continue", "exec"}, wantCont: true, wantRest: []string{"exec"}},
		{name: "flag not id", args: []string{"--continue", "--theme", "auto"}, wantCont: true, wantRest: []string{"--theme", "auto"}},
		{name: "continue eq empty", args: []string{"--continue="}, wantErr: "requires a session id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, rest, err := splitLeadingResumeFlags(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if flags.continueSet != tc.wantCont || flags.continueID != tc.wantID {
				t.Fatalf("continue = %v/%q, want %v/%q", flags.continueSet, flags.continueID, tc.wantCont, tc.wantID)
			}
			if flags.resumeSet != tc.wantRes || flags.resumeID != tc.wantRID {
				t.Fatalf("resume = %v/%q, want %v/%q", flags.resumeSet, flags.resumeID, tc.wantRes, tc.wantRID)
			}
			if len(rest) != len(tc.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tc.wantRest)
			}
			for i := range rest {
				if rest[i] != tc.wantRest[i] {
					t.Fatalf("rest = %v, want %v", rest, tc.wantRest)
				}
			}
		})
	}
}

func resumeTestStore(t *testing.T, cwd string) *sessions.Store {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	if _, err := store.Create(sessions.CreateInput{SessionID: "sess_ok_1", Title: "ok", Cwd: cwd, ModelID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("sess_ok_1", sessions.AppendEventInput{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(sessions.CreateInput{SessionID: "sess_child_1", SessionKind: sessions.SessionKindChild, Title: "child", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestResolveRootResumeValidation(t *testing.T) {
	cwd := t.TempDir()
	store := resumeTestStore(t, cwd)
	deps := appDeps{newSessionStore: func() *sessions.Store { return store }}
	if id, picker, err := resolveRootResume(rootFlags{continueSet: true, continueID: "sess_ok_1"}, deps); err != nil || id != "sess_ok_1" || picker {
		t.Fatalf("valid id: id=%q picker=%v err=%v", id, picker, err)
	}
	if _, _, err := resolveRootResume(rootFlags{continueSet: true, continueID: "missing_nope"}, deps); err == nil || !strings.Contains(err.Error(), "não encontrada") {
		t.Fatalf("missing id err = %v", err)
	}
	if _, _, err := resolveRootResume(rootFlags{resumeSet: true, resumeID: "sess_child_1"}, deps); err == nil || !strings.Contains(err.Error(), "não pode ser retomada") {
		t.Fatalf("child kind err = %v", err)
	}
	if _, _, err := resolveRootResume(rootFlags{continueSet: true, continueID: "bad id!!!"}, deps); err == nil || !strings.Contains(err.Error(), "inválida") {
		t.Fatalf("bad format err = %v", err)
	}
	if id, picker, err := resolveRootResume(rootFlags{continueSet: true}, deps); err != nil || id != "latest" || picker {
		t.Fatalf("bare continue: id=%q picker=%v err=%v", id, picker, err)
	}
	if id, picker, err := resolveRootResume(rootFlags{resumeSet: true}, deps); err != nil || id != "" || !picker {
		t.Fatalf("bare resume: id=%q picker=%v err=%v", id, picker, err)
	}
	empty := appDeps{newSessionStore: func() *sessions.Store {
		return sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	}}
	if _, _, err := resolveRootResume(rootFlags{resumeSet: true}, empty); err == nil || !strings.Contains(err.Error(), "nenhuma sessão") {
		t.Fatalf("empty picker err = %v", err)
	}
}

func TestRunContinueLaunchesTUIWithSession(t *testing.T) {
	cwd := t.TempDir()
	store := resumeTestStore(t, cwd)
	for _, args := range [][]string{
		{"--continue", "sess_ok_1"},
		{"-c", "sess_ok_1"},
		{"--resume", "sess_ok_1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var launched tui.Options
			code := runWithDeps(args, &stdout, &stderr, appDeps{
				getwd: func() (string, error) { return cwd, nil },
				resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
					return usableResolvedConfig(3), nil
				},
				newSessionStore: func() *sessions.Store { return store },
				runTUI: func(_ context.Context, options tui.Options) int {
					launched = options
					return 0
				},
			})
			if code != 0 {
				t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
			}
			if launched.ResumeSessionID != "sess_ok_1" {
				t.Fatalf("ResumeSessionID = %q", launched.ResumeSessionID)
			}
		})
	}
}

func TestRunContinueBareResolvesLatestAndPicker(t *testing.T) {
	cwd := t.TempDir()
	store := resumeTestStore(t, cwd)
	stub := func(t *testing.T, args []string) tui.Options {
		t.Helper()
		var stdout, stderr bytes.Buffer
		var launched tui.Options
		code := runWithDeps(args, &stdout, &stderr, appDeps{
			getwd: func() (string, error) { return cwd, nil },
			resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
				return usableResolvedConfig(3), nil
			},
			newSessionStore: func() *sessions.Store { return store },
			runTUI: func(_ context.Context, options tui.Options) int {
				launched = options
				return 0
			},
		})
		if code != 0 {
			t.Fatalf("%v: exit = %d: %s", args, code, stderr.String())
		}
		return launched
	}
	if got := stub(t, []string{"--continue"}); got.ResumeSessionID != "latest" || got.ShowResumePicker {
		t.Fatalf("bare --continue: %+v", got)
	}
	if got := stub(t, []string{"--resume"}); got.ResumeSessionID != "" || !got.ShowResumePicker {
		t.Fatalf("bare --resume: %+v", got)
	}
}

func TestRunContinueInvalidIDExitsNonZero(t *testing.T) {
	cwd := t.TempDir()
	store := resumeTestStore(t, cwd)
	for _, args := range [][]string{
		{"--continue", "missing_nope"},
		{"--resume", "sess_child_1"},
		{"--continue", "bad id!!!"},
		{"--continue", "exec", "--trace", "x"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			launched := false
			code := runWithDeps(args, &stdout, &stderr, appDeps{
				getwd: func() (string, error) { return cwd, nil },
				resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
					return usableResolvedConfig(3), nil
				},
				newSessionStore: func() *sessions.Store { return store },
				runTUI: func(_ context.Context, options tui.Options) int {
					launched = true
					return 0
				},
			})
			if code == 0 {
				t.Fatal("expected non-zero exit")
			}
			if launched {
				t.Fatal("must not open a session on invalid id")
			}
			if strings.TrimSpace(stderr.String()) == "" {
				t.Fatal("expected a clear stderr message")
			}
		})
	}
}

func TestHelpAndCompletionsMentionResume(t *testing.T) {
	var stdout bytes.Buffer
	if code := runWithDeps([]string{"--help"}, &stdout, &bytes.Buffer{}, appDeps{}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"--continue", "--resume", "-c"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
}
