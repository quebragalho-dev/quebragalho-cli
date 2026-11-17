package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/execprofile"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// TestTurnBudgetsPinnedValues pins the three turn budgets the audit found and
// the order in which they apply. Values and precedence are intentional — a
// change to any literal below, or to which source wins, fails this test by
// design:
//
//	12  agent loop fallback: bare agent.Run with MaxTurns unset (<= 0).
//	    Never overrides a resolved CLI/TUI run; only keeps direct
//	    programmatic runs finite.
//	80  interactive default: resolved config budget (TUI sessions).
//	500 absolute ceiling: clamps QG_MAX_TURNS, /turns, and config values, and
//	    doubles as the headless exec default when nothing is configured.
//
// Precedence, highest first: explicit pin (--max-turns, mode preset fill,
// QG_MAX_TURNS, config file) > exec profile (only when unpinned) > mode
// default (exec 500 / interactive 80) > loop fallback 12 (only when unset).
func TestTurnBudgetsPinnedValues(t *testing.T) {
	// The single definitions must hold these exact values.
	if config.MaxTurnsCeiling != 500 {
		t.Fatalf("MaxTurnsCeiling = %d, want 500", config.MaxTurnsCeiling)
	}
	if agent.FallbackMaxTurns != 12 {
		t.Fatalf("FallbackMaxTurns = %d, want 12", agent.FallbackMaxTurns)
	}

	// An unconfigured resolve carries the interactive default, unset.
	resolved, err := config.Resolve(config.ResolveOptions{Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MaxTurns != 80 || resolved.MaxTurnsSet {
		t.Fatalf("unconfigured resolve = (%d, set=%v), want (80, false)", resolved.MaxTurns, resolved.MaxTurnsSet)
	}

	// Env beats the file; garbage env is ignored; over-ceiling env clamps.
	file := filepath.Join(t.TempDir(), "qg.json")
	if err := os.WriteFile(file, []byte(`{"maxTurns":40}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	resolved, err = config.Resolve(config.ResolveOptions{
		UserConfigPath: file,
		Env:            map[string]string{config.MaxTurnsEnv: "120"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MaxTurns != 120 || !resolved.MaxTurnsSet {
		t.Fatalf("env over file = (%d, set=%v), want (120, true)", resolved.MaxTurns, resolved.MaxTurnsSet)
	}
	resolved, err = config.Resolve(config.ResolveOptions{
		UserConfigPath: file,
		Env:            map[string]string{config.MaxTurnsEnv: "999999"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MaxTurns != 500 {
		t.Fatalf("over-ceiling env = %d, want clamped to 500", resolved.MaxTurns)
	}
	resolved, err = config.Resolve(config.ResolveOptions{
		UserConfigPath: file,
		Env:            map[string]string{config.MaxTurnsEnv: "not-a-number"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MaxTurns != 40 {
		t.Fatalf("garbage env over file = %d, want file value 40", resolved.MaxTurns)
	}

	// CLI overrides (explicit --max-turns) beat env.
	resolved, err = config.Resolve(config.ResolveOptions{
		UserConfigPath: file,
		Env:            map[string]string{config.MaxTurnsEnv: "120"},
		Overrides:      config.Overrides{MaxTurns: 60},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MaxTurns != 60 {
		t.Fatalf("override over env = %d, want 60", resolved.MaxTurns)
	}

	// Headless default: unconfigured resolves to the ceiling; a set budget
	// (even 80, the interactive default chosen on purpose) passes through.
	if got := execTurnBudget(config.ResolvedConfig{MaxTurns: 80, MaxTurnsSet: false}); got != 500 {
		t.Fatalf("execTurnBudget unset = %d, want 500", got)
	}
	if got := execTurnBudget(config.ResolvedConfig{MaxTurns: 80, MaxTurnsSet: true}); got != 80 {
		t.Fatalf("execTurnBudget set 80 = %d, want 80", got)
	}

	// Exec profile: displaces the resolved budget only when nothing is pinned.
	// A pin reaches the resolved budget through overrides (asserted above), so
	// the profile sees it in resolvedMaxTurns and backs off entirely; without
	// a pin the profile displaces the resolved budget.
	fast, _ := execprofile.Lookup("fast")
	if effective, displaced := applyProfileTurnBudget(fast, 0, 80); effective != 30 || displaced != 80 {
		t.Fatalf("fast over resolved 80 = (%d, %d), want (30, 80)", effective, displaced)
	}
	if effective, displaced := applyProfileTurnBudget(fast, resolved.MaxTurns, resolved.MaxTurns); effective != 60 || displaced != 0 {
		t.Fatalf("fast over pinned 60 = (%d, %d), want (60, 0)", effective, displaced)
	}
}

// pinStubProvider replays scripted single-tool-call turns, then goes silent
// (Done with no text and no calls), mirroring agent's mockProvider: turns
// beyond the scripted ones end the run without doing work.
type pinStubProvider struct {
	turns    [][]qgruntime.StreamEvent
	requests int
}

func (p *pinStubProvider) StreamCompletion(ctx context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	p.requests++
	events := []qgruntime.StreamEvent{{Type: qgruntime.StreamEventDone}}
	if p.requests <= len(p.turns) {
		events = p.turns[p.requests-1]
	}
	ch := make(chan qgruntime.StreamEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func pinReadTurn(t *testing.T, id string) []qgruntime.StreamEvent {
	t.Helper()
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: id, ToolName: "read_file"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: id, ArgumentsFragment: `{"path":"notes.txt"}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: id},
		{Type: qgruntime.StreamEventDone},
	}
}

// TestTurnFallbackBeatsNothing pins the loop's last-resort budget: a bare
// agent.Run with MaxTurns unset runs exactly 12 tool turns, and any explicit
// MaxTurns wins over the fallback.
func TestTurnFallbackBeatsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	newRegistry := func() *tools.Registry {
		registry := tools.NewRegistry()
		registry.Register(tools.NewScopedReadFileTool(root, nil))
		return registry
	}
	newProvider := func() *pinStubProvider {
		turns := make([][]qgruntime.StreamEvent, 12)
		for i := range turns {
			turns[i] = pinReadTurn(t, fmt.Sprintf("call-%d", i+1))
		}
		return &pinStubProvider{turns: turns}
	}

	// Unset budget: the 12 scripted tool turns run, the silent finalization
	// turn yields the max-turns answer.
	result, err := agent.Run(context.Background(), "loop", newProvider(), agent.Options{
		Registry: newRegistry(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns != 12 {
		t.Fatalf("unset MaxTurns ran %d turns, want the 12-turn fallback", result.Turns)
	}
	if result.FinalAnswer != "Agent reached maximum number of turns without a final answer." {
		t.Fatalf("unset MaxTurns answer = %q, want the max-turns answer", result.FinalAnswer)
	}

	// Explicit budget beats the fallback.
	result, err = agent.Run(context.Background(), "loop", newProvider(), agent.Options{
		Registry: newRegistry(),
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns != 5 {
		t.Fatalf("MaxTurns 5 ran %d turns, want 5 (explicit beats fallback)", result.Turns)
	}
}
