package tui

import (
	"context"
	"strings"
	"testing"
)

// TestSandboxWarningAppearsOnceAtStartup pins the TUI half of the session
// warning: a non-empty SandboxWarning renders as a system message exactly
// once at startup, so a user who never runs `qg doctor` still learns that
// commands run without native isolation.
func TestSandboxWarningAppearsOnceAtStartup(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd:            t.TempDir(),
		SandboxWarning: "Ambiente isolado indisponível: os comandos rodam sem isolamento nativo.",
	})
	if got := strings.Count(transcriptText(m.transcript), "Ambiente isolado"); got != 1 {
		t.Fatalf("sandbox warning appears %d times, want exactly 1: %q", got, transcriptText(m.transcript))
	}
}

// TestSandboxWarningAbsentStaysSilent covers the quiet paths: an empty
// warning (backend works, sandbox disabled, or already isolated) adds no
// transcript row.
func TestSandboxWarningAbsentStaysSilent(t *testing.T) {
	m := newModel(context.Background(), Options{Cwd: t.TempDir()})
	if transcriptContains(m.transcript, "Ambiente isolado") {
		t.Fatalf("empty sandbox warning must stay silent: %q", transcriptText(m.transcript))
	}
}
