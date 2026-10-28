package installtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGH stands in for the gh CLI. FAKE_WAITING / FAKE_PENDING control what the
// two read calls return; every invocation is appended to $FAKE_LOG.
const fakeGH = `#!/usr/bin/env bash
echo "$*" >> "$FAKE_LOG"
case "$1" in
  run)
    [ "${FAKE_WAITING:-1}" = 1 ] && echo 98765
    ;;
  api)
    case "$*" in
      *"-X POST"*) ;;
      *) [ "${FAKE_PENDING:-1}" = 1 ] && echo 555 ;;
    esac
    ;;
esac
exit 0
`

func runApprove(t *testing.T, env []string, args ...string) (output string, log string, err error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	if writeErr := os.WriteFile(ghPath, []byte(fakeGH), 0o755); writeErr != nil {
		t.Fatal(writeErr)
	}
	logPath := filepath.Join(dir, "gh.log")
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts/approve-npm-publish.sh")}, args...)...)
	cmd.Env = append(os.Environ(), append(env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_LOG="+logPath)...)
	var combined bytes.Buffer
	cmd.Stdout, cmd.Stderr = &combined, &combined
	err = cmd.Run()
	logData, _ := os.ReadFile(logPath)
	return combined.String(), string(logData), err
}

func TestApproveScriptApprovesCLIReleaseByDefault(t *testing.T) {
	out, log, err := runApprove(t, nil, "v1.2.3")
	if err != nil {
		t.Fatalf("approve failed: %v\n%s", err, out)
	}
	containsAll(t, log, []string{
		"--workflow release-artifacts.yml --branch v1.2.3",
		`environment.name=="npm-publish"`,
		"-X POST repos/quebragalho-dev/quebragalho-cli/actions/runs/98765/pending_deployments",
		"environment_ids[]=555",
		"state=approved",
	})
	if !strings.Contains(out, "approved npm-publish for v1.2.3 (run 98765)") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestApproveScriptTargetsSDKWorkflowAndEnvironment(t *testing.T) {
	out, log, err := runApprove(t, []string{"QG_REPO=other/fork"}, "sdk-v0.1.1", "sdk-publish.yml", "sdk-publish")
	if err != nil {
		t.Fatalf("approve failed: %v\n%s", err, out)
	}
	containsAll(t, log, []string{
		"--repo other/fork --workflow sdk-publish.yml --branch sdk-v0.1.1",
		`environment.name=="sdk-publish"`,
		"-X POST repos/other/fork/actions/runs/98765/pending_deployments",
	})
}

func TestApproveScriptRefusesWhenNothingIsWaiting(t *testing.T) {
	for name, env := range map[string][]string{
		"no waiting run":        {"FAKE_WAITING=0"},
		"no pending deployment": {"FAKE_PENDING=0"},
	} {
		t.Run(name, func(t *testing.T) {
			out, log, err := runApprove(t, env, "v1.2.3")
			if err == nil {
				t.Fatalf("expected failure, got success:\n%s", out)
			}
			if strings.Contains(log, "-X POST") {
				t.Fatalf("must not approve anything, log:\n%s", log)
			}
		})
	}
}

func TestApproveScriptRequiresATag(t *testing.T) {
	out, _, err := runApprove(t, nil)
	if err == nil || !strings.Contains(out, "usage:") {
		t.Fatalf("want usage error, got err=%v out=%q", err, out)
	}
}
