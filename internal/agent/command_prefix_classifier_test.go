package agent

import (
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
