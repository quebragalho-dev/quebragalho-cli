package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// TestKnownSafeCommandSegmentRejectsGitOptionsThatRunProgramsOrWriteFiles
// ensures git global options that redirect configuration or helper programs,
// and subcommand options that run a program or write a file, are never
// classified as known-safe.
func TestKnownSafeCommandSegmentRejectsGitOptionsThatRunProgramsOrWriteFiles(t *testing.T) {
	cases := [][]string{
		{"git", "-c", "core.fsmonitor=./payload.sh", "status"},
		{"git", "-ccore.fsmonitor=./payload.sh", "status"},
		{"git", "--config-env", "core.fsmonitor=PAYLOAD", "status"},
		{"git", "--config-env=core.fsmonitor=PAYLOAD", "status"},
		{"git", "--exec-path=./bin", "status"},
		{"git", "--git-dir=./other", "status"},
		{"git", "--git-dir", "./other", "status"},
		{"git", "--work-tree=./other", "diff"},
		{"git", "--namespace=x", "log"},
		{"git", "--super-prefix=x", "status"},
		{"git", "diff", "--ext-diff"},
		{"git", "log", "-p", "--ext-diff"},
		{"git", "show", "--textconv", "HEAD"},
		{"git", "diff", "--ext"},
		{"git", "diff", "--outp=leak.txt"},
	}
	for _, command := range cases {
		t.Run(command[len(command)-1]+"_"+command[1], func(t *testing.T) {
			if knownSafeCommandSegment(command) {
				t.Fatalf("expected %q to be unsafe, got known-safe", command)
			}
		})
	}
}

// TestMatchCommandPrefixDoesNotCarryGitConfigOverride ensures a git segment
// carrying a config override does not ride along an approved prefix outside
// the sandbox.
func TestMatchCommandPrefixDoesNotCarryGitConfigOverride(t *testing.T) {
	engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: t.TempDir()})
	engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})

	command := "ps aux && git -c core.fsmonitor=./payload.sh status"
	if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": command}, Options{Sandbox: engine}); ok {
		t.Fatalf("match for %q succeeded, want no match because git config override is not known-safe", command)
	}
}

// TestKnownSafeCommandSegmentKeepsReadOnlyForms pins the behavior of plain
// read-only forms, which must stay known-safe exactly as today.
func TestKnownSafeCommandSegmentKeepsReadOnlyForms(t *testing.T) {
	cases := []struct {
		command []string
		want    bool
	}{
		{[]string{"git", "status"}, true},
		{[]string{"git", "status", "--short"}, true},
		{[]string{"git", "log", "--oneline", "-5"}, true},
		{[]string{"git", "diff", "--stat"}, true},
		{[]string{"git", "diff", "HEAD~1"}, true},
		{[]string{"git", "show", "HEAD"}, true},
		{[]string{"git", "branch"}, true},
		{[]string{"git", "branch", "--show-current"}, true},
		{[]string{"git", "--no-pager", "log", "-3"}, true},
		{[]string{"git", "-P", "diff"}, true},
		{[]string{"rg", "foo"}, true},
		{[]string{"base64", "file.bin"}, true},
		{[]string{"find", ".", "-name", "x.go"}, true},
		{[]string{"sed", "-n", "1,5p", "file.txt"}, true},
		{[]string{"git", "-C", "/tmp", "status"}, false},
		{[]string{"git", "--upload-pack=x", "status"}, false},
		{[]string{"git", "diff", "--output=leak.txt"}, false},
		{[]string{"git", "diff", "--output", "leak.txt"}, false},
		{[]string{"git", "log", "--exec=x"}, false},
		{[]string{"git", "branch", "-D", "x"}, false},
		{[]string{"git", "push"}, false},
		{[]string{"git", "commit", "-m", "x"}, false},
		{[]string{"git"}, false},
		{[]string{"rg", "--pre", "cat", "foo"}, false},
		{[]string{"rg", "--pre=cat", "foo"}, false},
		{[]string{"rg", "-z", "foo"}, false},
		{[]string{"rg", "--search-zip", "foo"}, false},
		{[]string{"rg", "--hostname-bin", "x", "foo"}, false},
		{[]string{"base64", "-o", "out", "file.bin"}, false},
		{[]string{"base64", "--output=out", "file.bin"}, false},
		{[]string{"base64", "-oout", "file.bin"}, false},
		{[]string{"find", ".", "-delete"}, false},
		{[]string{"find", ".", "-exec", "echo", "x"}, false},
		{[]string{"sed", "-i", "s/a/b/", "file.txt"}, false},
		{[]string{"sed", "-n", "1d", "file.txt"}, false},
		{[]string{"sed", "1,5p", "file.txt"}, false},
	}
	for _, testCase := range cases {
		want := testCase.want && (runtime.GOOS != "windows" || !tools.MsysProneCommandName(testCase.command[0]))
		if got := knownSafeCommandSegment(testCase.command); got != want {
			t.Errorf("knownSafeCommandSegment(%q) = %v, want %v", testCase.command, got, want)
		}
	}
}

func TestMatchCommandPrefixChecksEffectiveGitRepoConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary unavailable")
	}
	hermeticGitEnv(t)

	initRepo := func(t *testing.T, dir string, configLines string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", dir, err, out)
		}
		if configLines != "" {
			cfgPath := filepath.Join(dir, ".git", "config")
			existing, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfgPath, append(existing, []byte("\n"+configLines)...), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("clean nested repo stays automatic", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		initRepo(t, filepath.Join(root, "sub"), "[user]\n\tname = Dev\n[core]\n\tfsmonitor = true\n")

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); !ok {
			t.Fatal("expected clean nested repo read-only git status to remain automatic")
		}
	})

	unsafeConfigs := []struct {
		name   string
		config string
	}{
		{"core.fsmonitor", "[core]\n\tfsmonitor = ./payload.sh\n"},
		{"core.pager", "[core]\n\tpager = ./payload.sh\n"},
		{"pager.status", "[pager]\n\tstatus = ./payload.sh\n"},
		{"diff.external", "[diff]\n\texternal = ./payload.sh\n"},
		{"diff.custom.command", "[diff \"custom\"]\n\tcommand = ./payload.sh\n"},
		{"diff.custom.textconv", "[diff \"custom\"]\n\ttextconv = ./payload.sh\n"},
		{"filter.evil.clean", "[filter \"evil\"]\n\tclean = ./payload.sh\n"},
		{"filter.evil.smudge", "[filter \"evil\"]\n\tsmudge = ./payload.sh\n"},
		{"filter.evil.process", "[filter \"evil\"]\n\tprocess = ./payload.sh\n"},
		{"core.sshCommand", "[core]\n\tsshCommand = ./payload.sh\n"},
		{"credential.helper", "[credential]\n\thelper = ./payload.sh\n"},
		{"gpg.program", "[gpg]\n\tprogram = ./payload.sh\n"},
		{"core.hooksPath", "[core]\n\thooksPath = .githooks\n"},
	}

	for _, tc := range unsafeConfigs {
		t.Run("nested repo "+tc.name+" requires approval", func(t *testing.T) {
			root := t.TempDir()
			initRepo(t, root, "")
			initRepo(t, filepath.Join(root, "sub"), tc.config)

			engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
			engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})

			if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
				t.Fatalf("matchCommandPrefix succeeded for unsafe git config %s", tc.name)
			}
			if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && git status", "cwd": "sub"}, Options{Sandbox: engine, Cwd: root}); ok {
				t.Fatalf("matchCommandPrefix succeeded for cwd=sub with unsafe git config %s", tc.name)
			}
			if prefix := proposedCommandPrefixInDir("bash", map[string]any{"command": "ps aux && cd sub && git status"}, root); prefix != nil {
				t.Fatalf("proposedCommandPrefixInDir = %#v, want nil for unsafe nested git config %s", prefix, tc.name)
			}
			if prefix := proposedCommandPrefixInDir("bash", map[string]any{"command": "cd sub && git status"}, root); prefix != nil {
				t.Fatalf("proposedCommandPrefixInDir = %#v, want nil so unsafe git config %s is not proposed as prefix", prefix, tc.name)
			}
		})
	}

	t.Run("included config requires approval", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		sub := filepath.Join(root, "sub")
		initRepo(t, sub, "")
		incPath := filepath.Join(sub, ".git", "included.cfg")
		if err := os.WriteFile(incPath, []byte("[core]\n\tfsmonitor = ./evil.sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(sub, ".git", "config")
		existing, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, append(existing, []byte("\n[include]\n\tpath = included.cfg\n")...), 0o644); err != nil {
			t.Fatal(err)
		}

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("matchCommandPrefix succeeded despite unsafe [include] git config")
		}
	})

	t.Run("submodule config requires approval", func(t *testing.T) {
		root := t.TempDir()
		mainRepo := filepath.Join(root, "main")
		subSrc := filepath.Join(root, "subsrc")
		initRepo(t, mainRepo, "")
		initRepo(t, subSrc, "")
		for _, args := range [][]string{
			{"-C", mainRepo, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "commit", "--allow-empty", "-m", "init"},
			{"-C", subSrc, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "commit", "--allow-empty", "-m", "sub"},
			{"-C", mainRepo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub"},
			{"-C", filepath.Join(mainRepo, "sub"), "config", "diff.external", "./evil.sh"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: mainRepo})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git diff"}, Options{Sandbox: engine, Cwd: mainRepo}); ok {
			t.Fatal("matchCommandPrefix succeeded despite unsafe submodule git config")
		}
	})

	t.Run("pipe subshell cd does not hide unsafe directory or pollute outer dir", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		initRepo(t, filepath.Join(root, "sub"), "[core]\n\tfsmonitor = ./payload.sh\n")

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		// `cd sub` in pipe runs in a subshell; outer `git status` still runs in clean `root`.
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub | true && git status"}, Options{Sandbox: engine, Cwd: root}); !ok {
			t.Fatal("pipe subshell cd should not affect outer git status in clean root")
		}
		// `cd sub` before pipe runs `git status` inside unsafe `sub`.
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && echo ok | git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("git status inside pipe after `cd sub` must see unsafe sub config")
		}
	})

	t.Run("emptying credential.helper restores safety", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		initRepo(t, filepath.Join(root, "sub"), "[credential]\n\thelper = ./evil.sh\n\thelper =\n[pager]\n\tstatus = true\n")

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); !ok {
			t.Fatal("reset credential.helper = empty and boolean pager.status should keep read-only git automatic")
		}
	})

	t.Run("worktree config scope requires approval", func(t *testing.T) {
		root := t.TempDir()
		mainRepo := filepath.Join(root, "main")
		wtDir := filepath.Join(root, "wt")
		initRepo(t, mainRepo, "")
		for _, args := range [][]string{
			{"-C", mainRepo, "-c", "user.name=Dev", "-c", "user.email=dev@example.com", "commit", "--allow-empty", "-m", "init"},
			{"-C", mainRepo, "config", "extensions.worktreeConfig", "true"},
			{"-C", mainRepo, "worktree", "add", "-q", wtDir},
			{"-C", wtDir, "config", "--worktree", "core.fsmonitor", "./evil.sh"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd wt && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("matchCommandPrefix succeeded despite unsafe worktree git config")
		}
	})

	t.Run("existing git status grant does not bypass unsafe nested repo config", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		initRepo(t, filepath.Join(root, "sub"), "[core]\n\tfsmonitor = ./evil.sh\n")

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"git", "status"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("git status grant from parent repo must not auto-allow unsafe nested repo")
		}
	})

	t.Run("nested repo executable post-index-change hook requires approval", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		sub := filepath.Join(root, "sub")
		initRepo(t, sub, "")
		hookPath := filepath.Join(sub, ".git", "hooks", "post-index-change")
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ntouch evil\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("matchCommandPrefix succeeded despite executable post-index-change hook")
		}
		if prefix := proposedCommandPrefixInDir("bash", map[string]any{"command": "cd sub && git status"}, root); prefix != nil {
			t.Fatalf("proposedCommandPrefixInDir = %#v, want nil with executable post-index-change hook", prefix)
		}
	})

	t.Run("global relative core.hooksPath with post-index-change hook requires approval", func(t *testing.T) {
		root := t.TempDir()
		initRepo(t, root, "")
		sub := filepath.Join(root, "sub")
		initRepo(t, sub, "")
		globalCfg := os.Getenv("GIT_CONFIG_GLOBAL")
		if err := os.WriteFile(globalCfg, []byte("[core]\n\thooksPath = .githooks\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root})
		engine.GrantCommandPrefixForSession("bash", []string{"ps", "aux"})
		// Before .githooks/post-index-change exists, the clean repo remains automatic.
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); !ok {
			t.Fatal("clean repo with global relative core.hooksPath and no post-index-change hook should stay automatic")
		}

		hookPath := filepath.Join(sub, ".githooks", "post-index-change")
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ntouch evil\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := matchCommandPrefix("bash", map[string]any{"command": "ps aux && cd sub && git status"}, Options{Sandbox: engine, Cwd: root}); ok {
			t.Fatal("matchCommandPrefix succeeded despite post-index-change via global relative core.hooksPath")
		}
	})
}

func hermeticGitEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}
