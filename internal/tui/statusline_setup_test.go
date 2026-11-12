package tui

import (
	"encoding/json"
	"os"
	"runtime"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// /statusline parses to its own command kind.
func TestParseStatuslineCommand(t *testing.T) {
	if got := parseCommand("/statusline"); got.kind != commandStatusline {
		t.Fatalf("kind = %v, want commandStatusline", got.kind)
	}
	if got := parseCommand("/statusline reset"); got.kind != commandStatusline || strings.TrimSpace(got.text) != "reset" {
		t.Fatalf("got = %+v, want statusline/reset", got)
	}
}

// Bare /statusline shows the current config plus help, never launching a turn.
func TestStatuslineBareShowsStatus(t *testing.T) {
	m := limeTestModel()
	updated, cmd := m.dispatchCommand(parseCommand("/statusline"))
	if cmd != nil {
		t.Fatal("bare /statusline returned a command, want none")
	}
	transcript := updated.(model).transcript
	last := transcript[len(transcript)-1].text
	for _, want := range []string{"Statusline", "/statusline reset", "command"} {
		if !strings.Contains(last, want) {
			t.Fatalf("status card = %q, missing %q", last, want)
		}
	}
}

// The status card abbreviates $HOME with ~ and truncates long commands in
// the middle, so the script filename survives even narrow cards.
func TestStatuslineCardAbbreviatesAndMiddleTruncates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	deep := filepath.Join(home, "a", "very", "deep", "tree")
	script := filepath.Join(deep, "meu-rodape.sh")
	for _, width := range []int{50, 80, 140} {
		m := limeTestModel()
		m.width = width
		m.statuslineCfg = config.StatusLineConfig{
			Lines:   []config.StatusLineLine{{Left: []string{"mode"}}},
			Command: &config.StatusLineCommand{Command: "sh '" + script + "'"},
		}
		card := plainRender(t, m.statuslineStatusText())
		if !strings.Contains(card, "~") {
			t.Fatalf("width %d: card does not abbreviate home:\n%s", width, card)
		}
		if !strings.Contains(card, "meu-rodape.sh") {
			t.Fatalf("width %d: card lost the script filename:\n%s", width, card)
		}
		for _, line := range strings.Split(card, "\n") {
			if !strings.HasPrefix(line, "comando: ") {
				continue
			}
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d: comando line overflows: %q", width, line)
			}
		}
	}
}

// Applying a proposal with an active command and a stopped chain starts a
// run immediately and arms the tick chain; a running chain is never doubled.
func TestStatuslineApplyArmsTickChainWhenStopped(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.json")
	m := limeTestModel()
	m.width = 100
	m.userConfigPath = userPath
	proposal := statuslineProposal{Lines: []config.StatusLineLine{{Left: []string{"mode"}}}}
	cfg, _ := proposal.validatedConfig()
	cfg.Command = &config.StatusLineCommand{Command: "echo hi"}
	m.statuslinePending = &statuslinePendingApply{proposal: proposal, cfg: cfg, preview: "preview", stagingDir: dir}
	if m.statuslineTickArmed {
		t.Fatal("fresh model must not report an armed chain")
	}
	applied, cmd := m.applyStatuslinePending()
	if cmd == nil {
		t.Fatal("apply with an active command and a stopped chain must arm the chain")
	}
	nm := applied
	if !nm.statuslineTickArmed {
		t.Fatal("apply did not mark the chain armed")
	}
	ticked, tickCmd := nm.Update(execCmd(cmd))
	if tickCmd == nil {
		t.Fatal("armed tick must keep the chain alive")
	}
	if !ticked.(model).statuslineCmdInFlight {
		t.Fatal("first tick after apply must start the command run")
	}
}

// An already armed chain is left alone on apply: no second chain.
func TestStatuslineApplyKeepsRunningChain(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.json")
	m := limeTestModel()
	m.width = 100
	m.userConfigPath = userPath
	m.statuslineTickArmed = true
	proposal := statuslineProposal{Lines: []config.StatusLineLine{{Left: []string{"mode"}}}}
	cfg, _ := proposal.validatedConfig()
	cfg.Command = &config.StatusLineCommand{Command: "echo hi"}
	m.statuslinePending = &statuslinePendingApply{proposal: proposal, cfg: cfg, preview: "preview", stagingDir: dir}
	applied, cmd := m.applyStatuslinePending()
	if cmd != nil {
		t.Fatalf("apply over a running chain must not arm another one, got %T", cmd)
	}
	if !applied.statuslineTickArmed {
		t.Fatal("apply must not disarm the running chain")
	}
}

// /statusline reset clears the user config and the runtime state.
func TestStatuslineReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	seed := `{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}]}`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := config.SetStatusLine(path, config.StatusLineConfig{
		Lines: []config.StatusLineLine{{Left: []string{"mode"}}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := limeTestModel()
	m.userConfigPath = path
	m.statuslineCfg = config.StatusLineConfig{Lines: []config.StatusLineLine{{Left: []string{"mode"}}}}
	m.statuslineCmdOutput = "stale"
	updated, _ := m.dispatchCommand(parseCommand("/statusline reset"))
	nm := updated.(model)
	if nm.statuslineCfg.IsCustom() {
		t.Fatalf("config not cleared: %+v", nm.statuslineCfg)
	}
	if nm.statuslineCmdOutput != "" || nm.statuslineCmdErr != "" || nm.statuslineCmdInFlight {
		t.Fatal("runner state not cleared")
	}
	resolved, err := config.Resolve(config.ResolveOptions{UserConfigPath: path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.StatusLine.IsCustom() {
		t.Fatalf("file not cleared: %+v", resolved.StatusLine)
	}
}

// The setup prompt carries everything the model needs: catalog, current
// config, a real stdin example, width, and OS.
func TestStatuslineSetupPrompt(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelName = "model-x"
	prompt := m.statuslineSetupPrompt("/tmp/staging")
	for _, want := range []string{"mode", "effort", "context", "session_id", "staging", "linux", "windows", "proposal.json", runtime.GOOS} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("setup prompt missing %q", want)
		}
	}
}

// Unknown ids are dropped with a warning; known ones survive.
func TestStatuslineProposalValidation(t *testing.T) {
	proposal := statuslineProposal{
		Lines: []config.StatusLineLine{{Left: []string{"mode", "nope"}, Right: []string{"nah"}}},
	}
	cfg, warnings := proposal.validatedConfig()
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want both unknown ids", warnings)
	}
	if len(cfg.Lines) != 1 || len(cfg.Lines[0].Left) != 1 || cfg.Lines[0].Left[0] != "mode" {
		t.Fatalf("cfg = %+v, want only mode", cfg)
	}
	if _, warnings := (statuslineProposal{}).validatedConfig(); len(warnings) != 0 {
		t.Fatalf("empty proposal warns: %#v", warnings)
	}
}

// Script names stay inside staging: traversal is rejected.
func TestStatuslineStagedScriptContainment(t *testing.T) {
	staging := t.TempDir()
	if _, err := confinedStagingPath(staging, "../evil.sh"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := confinedStagingPath(staging, "ok.sh"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
}

// A setup turn never starts while quitting, mid-run, or twice.
func TestStatuslineSetupGuards(t *testing.T) {
	exiting := limeTestModel()
	exiting.exiting = true
	exiting.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	if _, cmd := exiting.dispatchCommand(parseCommand("/statusline faça algo")); cmd != nil {
		t.Fatal("setup launched while exiting")
	}
	busy := limeTestModel()
	busy.pending = true
	if _, cmd := busy.dispatchCommand(parseCommand("/statusline faça algo")); cmd != nil {
		t.Fatal("setup launched mid-run")
	}
	armed := limeTestModel()
	armed.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	armed.statuslineSetup = &statuslineSetupState{runID: 1}
	if _, cmd := armed.dispatchCommand(parseCommand("/statusline outra coisa")); cmd != nil {
		t.Fatal("second setup launched while one is armed")
	}
}

// A validação da config roda ANTES da prévia: proposta com refreshMs 500 é
// ajustada para 1000 com aviso, e o Enter aplica o ajustado — nunca exibe
// "Enter aplica" para depois falhar no Enter.
func TestStatuslinePreviewValidatesBeforeAsking(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.json")
	seed := `{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}]}`
	if err := os.WriteFile(userPath, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	proposal := `{"lines": [{"left": ["mode"]}], "refreshMs": 500}`
	if err := os.WriteFile(filepath.Join(staging, "proposal.json"), []byte(proposal), 0600); err != nil {
		t.Fatal(err)
	}
	m := limeTestModel()
	m.width = 100
	m.userConfigPath = userPath
	m.statuslineSetup = &statuslineSetupState{runID: 9, startedAt: time.Now().Add(-time.Minute), stagingDir: staging}
	updated, cmd := m.maybeFinalizeStatuslineSetup(agentResponseMsg{runID: 9})
	if cmd != nil {
		t.Fatal("fields-only proposal must preview synchronously")
	}
	nm := updated
	if nm.statuslinePending == nil {
		t.Fatal("no preview pending")
	}
	preview := nm.statuslinePending.preview
	for _, want := range []string{"1000", "Enter aplica"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview lacks %q:\n%s", want, preview)
		}
	}
	applied, _ := nm.Update(testKey(tea.KeyEnter))
	if applied.(model).statuslinePending != nil {
		t.Fatal("pending not cleared after Enter")
	}
	raw, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		StatusLine struct {
			RefreshMs int `json:"refreshMs"`
		} `json:"statusline"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.StatusLine.RefreshMs != 1000 {
		t.Fatalf("applied refreshMs = %d, want the adjusted 1000", doc.StatusLine.RefreshMs)
	}
}

// End to end: dispatching /statusline <texto> launches a real setup turn with
// the dedicated instruction, and its final message finalizes (here: no
// staging files, so a no-proposal notice and a cleared setup).
func TestStatuslineSetupTurnEndToEnd(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("entendido")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	updated, cmd := m.dispatchCommand(parseCommand("/statusline deixe azul"))
	nm := updated.(model)
	if nm.statuslineSetup == nil {
		t.Fatal("setup turn not armed")
	}
	if cmd == nil {
		t.Fatal("setup turn launched no command")
	}
	if len(provider.requests) != 0 {
		t.Fatal("provider hit before the turn ran")
	}
	msg, ok := execCmd(cmd).(agentResponseMsg)
	if !ok {
		t.Fatalf("turn returned %T, want agentResponseMsg", execCmd(cmd))
	}
	final, _ := nm.Update(msg)
	fm := final.(model)
	if fm.statuslineSetup != nil || fm.statuslinePending != nil {
		t.Fatal("setup not finalized after the turn")
	}
	last := fm.transcript[len(fm.transcript)-1].text
	if !strings.Contains(last, "não gerou") {
		t.Fatalf("notice = %q, want the no-proposal explanation", last)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 setup turn", len(provider.requests))
	}
	seen := false
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, "proposal.json") && strings.Contains(message.Content, "deixe azul") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("setup instruction (staging rules + user pedido) never reached the provider")
	}
}

// Finalizing with no staging files tells the user and clears the setup.
func TestStatuslineFinalizeWithoutStaging(t *testing.T) {
	m := limeTestModel()
	m.statuslineSetup = &statuslineSetupState{runID: 7, stagingDir: t.TempDir()}
	updated, cmd := m.maybeFinalizeStatuslineSetup(agentResponseMsg{runID: 7})
	if cmd != nil {
		t.Fatal("finalize returned a command, want none")
	}
	nm := updated
	if nm.statuslineSetup != nil || nm.statuslinePending != nil {
		t.Fatal("setup not cleared")
	}
	last := nm.transcript[len(nm.transcript)-1].text
	if !strings.Contains(last, "não gerou") {
		t.Fatalf("notice = %q, want a no-proposal explanation", last)
	}
}

// Fields-only proposals preview synchronously with real data and wait for
// Enter; Enter applies to the user config, Esc discards the staging.
func TestStatuslinePendingApplyAndCancel(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.json")
	seed := `{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}]}`
	if err := os.WriteFile(userPath, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := limeTestModel()
	m.userConfigPath = userPath
	proposal := statuslineProposal{Lines: []config.StatusLineLine{{Left: []string{"mode"}}}}
	cfg, _ := proposal.validatedConfig()
	preview := m.previewStatuslineProposal(&statuslinePendingApply{proposal: proposal, cfg: cfg}, nil)
	m.statuslinePending = &statuslinePendingApply{proposal: proposal, cfg: cfg, preview: preview, stagingDir: dir}
	if !strings.Contains(preview, "automático") {
		t.Fatalf("preview lacks real data:\n%s", preview)
	}
	applied, _ := m.Update(testKey(tea.KeyEnter))
	am := applied.(model)
	if am.statuslinePending != nil {
		t.Fatal("pending not cleared after Enter")
	}
	resolved, err := config.Resolve(config.ResolveOptions{UserConfigPath: userPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.StatusLine.Lines) != 1 {
		t.Fatalf("applied config = %+v", resolved.StatusLine)
	}

	m2 := limeTestModel()
	m2.userConfigPath = userPath
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "proposal.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	m2.statuslinePending = &statuslinePendingApply{proposal: proposal, preview: preview, stagingDir: staging}
	cancelled, _ := m2.Update(testKey(tea.KeyEsc))
	cm := cancelled.(model)
	if cm.statuslinePending != nil {
		t.Fatal("pending not cleared after Esc")
	}
	if _, err := os.Stat(filepath.Join(staging, "proposal.json")); !os.IsNotExist(err) {
		t.Fatal("staging not discarded after Esc")
	}
}
