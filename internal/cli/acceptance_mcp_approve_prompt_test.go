package cli

// `qg mcp approve` asks on the terminal before approving a project server,
// and the question shows everything the approval covers: command and args,
// env with values, header names, and the timeout and tool filters the
// project sets. Only what looks like a secret is masked.

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// acceptanceApprovePrompt runs `qg mcp approve <server>` with a terminal as
// stdin, types answer, and returns everything the command printed, on its
// outputs or on the terminal.
func acceptanceApprovePrompt(t *testing.T, repo, server, answer string) string {
	t.Helper()
	keyboard, tty, err := openAcceptancePTY()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = keyboard.Close() })
	var mu sync.Mutex
	var onTerminal bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buffer := make([]byte, 4096)
		for {
			n, err := keyboard.Read(buffer)
			mu.Lock()
			onTerminal.Write(buffer[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	if _, err := keyboard.Write([]byte(answer)); err != nil {
		t.Fatalf("type %q on the terminal: %v", answer, err)
	}

	var stdout, stderr bytes.Buffer
	deps := appDeps{getwd: func() (string, error) { return repo, nil }, stdin: tty}
	code := runWithDeps([]string{"mcp", "approve", server}, &stdout, &stderr, deps)
	_ = tty.Close()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}
	mu.Lock()
	printed := stdout.String() + stderr.String() + onTerminal.String()
	mu.Unlock()
	if code == exitSuccess {
		t.Fatalf("answering %q on a terminal approved %s:\n%s", strings.TrimSpace(answer), server, printed)
	}
	return printed
}

func TestAcceptanceMCPApprovePromptShowsTheWholeIdentity(t *testing.T) {
	acceptanceHermeticEnv(t)
	repo := acceptanceRepo(t, `{`+
		`"loc":{"type":"stdio","command":"node","args":["server.js","--api-key=sk-approve-27182"],`+
		`"env":{"NODE_OPTIONS":"--require ./.qg/hook.js","API_TOKEN":"tok-approve-31337"},`+
		`"timeout":600000,"enabled_tools":["alpha","beta"]},`+
		`"rem":{"type":"http","url":"https://mcp.example.test/mcp",`+
		`"headers":{"Authorization":"Bearer hdr-approve-16180","X-Org":"acme"}}}`, true)

	for _, check := range []struct {
		server string
		shown  []string
		hidden []string
	}{
		{"loc", []string{"node", "server.js", "--api-key", "NODE_OPTIONS", "--require ./.qg/hook.js", "API_TOKEN", "alpha", "beta"},
			[]string{"sk-approve-27182", "tok-approve-31337"}},
		{"rem", []string{"mcp.example.test", "Authorization", "X-Org"}, []string{"hdr-approve-16180"}},
	} {
		prompt := acceptanceApprovePrompt(t, repo, check.server, "n\n")
		var missing, leaked []string
		for _, want := range check.shown {
			if !strings.Contains(prompt, want) {
				missing = append(missing, want)
			}
		}
		if check.server == "loc" && !containsAnyFoldCLI(prompt, "600000", "600s", "10m", "10 min") {
			missing = append(missing, "the 10-minute timeout")
		}
		for _, secret := range check.hidden {
			if strings.Contains(prompt, secret) {
				leaked = append(leaked, secret)
			}
		}
		if len(missing) > 0 || len(leaked) > 0 {
			t.Errorf("approve prompt for %s: missing %q, secrets shown %q:\n%s", check.server, missing, leaked, prompt)
		}
	}
}
