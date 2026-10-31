package lsp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func stubTrust(t *testing.T, trusted bool, err error) {
	t.Helper()
	previous := isWorkspaceTrusted
	isWorkspaceTrusted = func(string) (bool, error) { return trusted, err }
	t.Cleanup(func() { isWorkspaceTrusted = previous })
}

type countingStarter struct {
	calls int
	roots []string
}

func (c *countingStarter) start(_ context.Context, _ []string, root string) (lspServer, error) {
	c.calls++
	c.roots = append(c.roots, root)
	// A missing binary is the cheapest way to end the start without a server.
	return nil, exec.ErrNotFound
}

func TestTrustGateBlocksServerStartInUntrustedWorkspace(t *testing.T) {
	stubTrust(t, false, nil)
	starter := &countingStarter{}
	m := newManagerWithStarter("/repo", trustGatedStarter(starter.start))

	diags, err := m.Check(context.Background(), "main.go", "package main\n")
	if err != nil || diags != nil {
		t.Fatalf("Check = (%v, %v), want silent no-diagnostics degrade", diags, err)
	}
	_, _, ok, navErr := m.Navigate(context.Background(), NavRequest{Op: NavDefinition, Path: "main.go", Text: "package main\n", Line: 1, Character: 1})
	if ok || !errors.Is(navErr, ErrWorkspaceUntrusted) {
		t.Fatalf("Navigate = (ok=%v, err=%v), want ErrWorkspaceUntrusted", ok, navErr)
	}
	if starter.calls != 0 {
		t.Fatalf("starter called %d times in an untrusted workspace, want 0", starter.calls)
	}
}

func TestTrustGateFailsClosedOnTrustStoreError(t *testing.T) {
	stubTrust(t, true, errors.New("trust store unreadable"))
	starter := &countingStarter{}
	m := newManagerWithStarter("/repo", trustGatedStarter(starter.start))

	if _, err := m.Check(context.Background(), "main.go", "package main\n"); err != nil {
		t.Fatalf("Check error = %v, want silent degrade", err)
	}
	if starter.calls != 0 {
		t.Fatalf("starter called %d times with an unreadable trust store, want 0", starter.calls)
	}
}

func TestTrustGateStartsServerInTrustedWorkspace(t *testing.T) {
	stubTrust(t, true, nil)
	starter := &countingStarter{}
	m := newManagerWithStarter("/repo", trustGatedStarter(starter.start))

	if _, err := m.Check(context.Background(), "main.go", "package main\n"); err != nil {
		t.Fatalf("Check error = %v", err)
	}
	if starter.calls != 1 || starter.roots[0] != "/repo" {
		t.Fatalf("starter calls = %d roots = %v, want one start rooted at /repo", starter.calls, starter.roots)
	}
}

// End to end through NewManager, the constructor every caller uses: a language
// server binary on PATH must not be executed at all in an untrusted workspace.
func TestNewManagerDoesNotSpawnServerInUntrustedWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake server fixture is a POSIX shell script")
	}
	command, ok := ServerFor("main.go")
	if !ok {
		t.Skip("no server configured for .go")
	}
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "spawned")
	script := "#!/bin/sh\ntouch '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, command[0]), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	t.Setenv("PATH", binDir)
	stubTrust(t, false, nil)

	workspace := t.TempDir()
	m := NewManager(workspace)
	if _, err := m.Check(context.Background(), filepath.Join(workspace, "main.go"), "package main\n"); err != nil {
		t.Fatalf("Check error = %v, want silent degrade", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("language server was spawned in an untrusted workspace")
	}
}
