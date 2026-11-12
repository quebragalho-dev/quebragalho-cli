// Acceptance tests for the subagents panel, "Subagentes visíveis, discretos"
// (spec approved 2026-11-11; exact strings from the Discreto, Ampliado and
// ChatSub frames). Decisions on top of the spec:
//   - Ctrl+G expands/collapses (Ctrl+T stays on cycleReasoning), so the hints
//     read "clique, ↓ ou Ctrl+G para ampliar" and "… · Ctrl+G recolhe";
//   - the subagent conversation follows the child live while it runs;
//   - the ? help fits a 40-row terminal with "? ou Esc fechar" always visible
//     (compact, or scrolling with the hint pinned).
//
// Run: go test ./internal/tui/ -run 'TestAcceptSubagents' -race -v
//
// Every input reaches the model the way a live run delivers it:
//   - specialistStartMsg keyed by the Task tool-call id, because the child
//     session does not exist yet when the tool call starts;
//   - then the parent-log specialist start event the executor writes (source,
//     childSessionId, toolCallId, startedAt) and, before the Task result, the
//     child's usage rollup;
//   - specialistProgressMsg and specialistCompleteMsg;
//   - specialistUsageMsg, the live usage ingress (contract below).
//
// OnToolProgress turns each stream-json usage event of the child into one
// specialistUsageMsg carrying the running totals of that Task call (the child
// emits per-call deltas; the producer sums them). Update prices the totals
// with the catalog and calls setUsage, which replaces the row's usage:
//
//	type specialistUsageMsg struct {
//		runID            int
//		toolCallID       string
//		promptTokens     int64  // cumulative for this Task call
//		completionTokens int64  // cumulative for this Task call
//		modelID          string // child model, from its run_start event
//	}
//
// Hermetic: fake HOME/XDG, QG_CRED_STORAGE=file, a dead local proxy for any
// accidental HTTP, a session store under t.TempDir() and a frozen clock. A
// tea.Cmd returned by Update runs only where a test says so (the stop
// command); no ticker, network or subprocess.
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

var (
	sub8Ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\a\x1b]*(?:\a|\x1b\\)`)
	// pt-BR estimate: tilde mandatory, comma decimal separator.
	sub8Money     = `~R\$ \d+,\d+`
	sub8CompactRe = regexp.MustCompile(`● (\d+) subagentes? · (\d+) rodando`)
	sub8PagingRe  = regexp.MustCompile(`(\d+) de (\d+) · ←→ outro subagente`)
)

const (
	sub8Hint          = "clique, ↓ ou Ctrl+G para ampliar"
	sub8Footer        = "↑↓ escolhe · Enter abre a conversa · x para (pede confirmação) · Ctrl+G recolhe"
	sub8ExpandedHdr   = "SUBAGENTES ·"
	sub8ChatSubFooter = "Esc volta para a conversa principal · x para este subagente"
	sub8HelpHint      = "? ou Esc fechar"
	sub8Parent        = "parent-1"
	sub8ModelID       = "modelo-teste"
)

// sub8Child is one Task subagent. The marker is the first message of its
// transcript, so a test can tell which conversation is on screen without
// trusting the header it is checking.
type sub8Child struct{ call, session, name, desc, marker string }

var (
	sub8Explorer = sub8Child{"call-explorer", "child-explorer", "explorer", "mapear o módulo de pagamentos", "Mapeie o módulo de pagamentos."}
	sub8Reviewer = sub8Child{"call-reviewer", "child-reviewer", "reviewer", "revisar tratamento de erros", "Revise o tratamento de erros."}
	sub8Planner  = sub8Child{"call-planner", "child-planner", "planner", "propor plano de testes", "Proponha um plano de testes."}
	sub8Children = []sub8Child{sub8Explorer, sub8Reviewer, sub8Planner}
)

func sub8Env(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv("QG_CRED_STORAGE", "file")
	t.Setenv("TERMUX_VERSION", "")
	t.Setenv("QG_REDUCED_MOTION", "1")
	t.Setenv("NO_COLOR", "1")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")
}

type sub8Clock struct{ now time.Time }

func (c *sub8Clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func sub8Append(t *testing.T, store *sessions.Store, sessionID string, kind sessions.EventType, payload map[string]any) {
	t.Helper()
	if _, err := store.AppendEvent(sessionID, sessions.AppendEventInput{Type: kind, Payload: payload}); err != nil {
		t.Fatalf("fixture: evento %s em %s: %v", kind, sessionID, err)
	}
}

// sub8Model is an offline model inside the parent session, with a frozen
// clock, the three child sessions (priced model, one marker message each), a
// priced catalog entry for the ~R$ estimate and reasoning efforts so Ctrl+T
// has something to cycle.
func sub8Model(t *testing.T) (model, *sub8Clock) {
	t.Helper()
	sub8Env(t)
	clock := &sub8Clock{now: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)}
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	if _, err := store.Create(sessions.CreateInput{SessionID: sub8Parent, ModelID: sub8ModelID}); err != nil {
		t.Fatalf("fixture: sessão pai: %v", err)
	}
	for _, child := range sub8Children {
		if _, err := store.Create(sessions.CreateInput{SessionID: child.session, ModelID: sub8ModelID}); err != nil {
			t.Fatalf("fixture: sessão %s: %v", child.session, err)
		}
		sub8Append(t, store, child.session, sessions.EventMessage, map[string]any{"role": "user", "content": child.marker})
	}
	m := newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		ProviderName: "qg",
		ModelName:    sub8ModelID,
		SessionStore: store,
	})
	m.width, m.height = 120, 40
	m.altScreen = false
	m.reducedMotion = true
	m.now = func() time.Time { return clock.now }
	m.activeSession = sessions.Metadata{SessionID: sub8Parent}
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"qg": {{
			ID: sub8ModelID, Source: "live",
			ReasoningEfforts: []string{"low", "medium", "high"},
			APIModel: &qgaccount.ModelInfo{
				ID: sub8ModelID,
				PricingBRL: qgaccount.ModelPricing{
					Currency: "BRL", Input: "0.5", Output: "1", CachedInput: "0.05",
				},
			},
		}},
	}
	return m, clock
}

func sub8View(m model) string {
	return sub8Ansi.ReplaceAllString(m.View().Content, "")
}

func sub8Key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: code}) }

func sub8Ctrl(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

func sub8Rune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
}

func sub8Update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(model)
	if !ok {
		t.Fatalf("Update(%T) devolveu %T, esperava model", msg, next)
	}
	return out
}

// sub8Start delivers a Task start in live order: the TUI hears the tool call
// first, then the executor records the child session in the parent log.
func sub8Start(t *testing.T, m model, clock *sub8Clock, child sub8Child, background bool) model {
	t.Helper()
	m = sub8Update(t, m, specialistStartMsg{runID: m.activeRunID, name: child.name, description: child.desc, childSessionID: child.call, background: background})
	sub8Append(t, m.sessionStore, sub8Parent, sessions.EventSpecialistStart, map[string]any{
		"source":         "specialist",
		"childSessionId": child.session,
		"taskId":         child.session,
		"toolCallId":     child.call,
		"specialist":     child.name,
		"description":    child.desc,
		"background":     background,
		"startedAt":      clock.now.UTC().Format(time.RFC3339Nano),
	})
	return m
}

func sub8Progress(t *testing.T, m model, child sub8Child, tool, detail string) model {
	t.Helper()
	return sub8Update(t, m, specialistProgressMsg{runID: m.activeRunID, toolCallID: child.call, toolName: tool, detail: detail})
}

func sub8Usage(t *testing.T, m model, child sub8Child, prompt, completion int64) model {
	t.Helper()
	return sub8Update(t, m, specialistUsageMsg{runID: m.activeRunID, toolCallID: child.call, promptTokens: prompt, completionTokens: completion, modelID: sub8ModelID})
}

// sub8Rollup appends the usage rollup the executor writes to the parent log
// when the child ends, before the Task result reaches the TUI.
func sub8Rollup(t *testing.T, m model, child sub8Child, prompt, completion int64) {
	t.Helper()
	sub8Append(t, m.sessionStore, sub8Parent, sessions.EventUsage, map[string]any{
		"source":           "specialist",
		"childSessionId":   child.session,
		"taskId":           child.session,
		"toolCallId":       child.call,
		"promptTokens":     prompt,
		"completionTokens": completion,
		"totalTokens":      prompt + completion,
		"usageEvents":      2,
	})
}

func sub8Complete(t *testing.T, m model, child sub8Child, status specialistStatus, errMsg string) model {
	t.Helper()
	return sub8Update(t, m, specialistCompleteMsg{runID: m.activeRunID, toolCallID: child.call, childSessionID: child.session, status: status, errorMsg: errMsg})
}

// sub8FrameTimeline reproduces the Ampliado frame: reviewer starts at T0 and
// finishes at T0+72s (1m12s, 8,1k tok); planner starts at T0+30s and fails
// at T0+70s (40s, 3,2k tok); explorer starts at T0+72s and is still reading
// payments/charge.go at T0+97s (25s, 12,0k tok). Totals: 1m37s since the
// first visible start and 23,3k tokens. Finished children report live usage
// while they run and their rollup at the end; the explorer only has live
// usage so far.
func sub8FrameTimeline(t *testing.T, m model, clock *sub8Clock) model {
	t.Helper()
	m = sub8Start(t, m, clock, sub8Reviewer, false)
	m = sub8Usage(t, m, sub8Reviewer, 3000, 1000)
	clock.advance(30 * time.Second)
	m = sub8Start(t, m, clock, sub8Planner, false)
	m = sub8Usage(t, m, sub8Planner, 1500, 500)
	clock.advance(40 * time.Second)
	m = sub8Usage(t, m, sub8Planner, 2400, 800)
	sub8Rollup(t, m, sub8Planner, 2400, 800)
	m = sub8Complete(t, m, sub8Planner, specialistError, "tempo esgotado no provedor")
	clock.advance(2 * time.Second)
	m = sub8Usage(t, m, sub8Reviewer, 6000, 2100)
	sub8Rollup(t, m, sub8Reviewer, 6000, 2100)
	m = sub8Complete(t, m, sub8Reviewer, specialistCompleted, "")
	m = sub8Start(t, m, clock, sub8Explorer, false)
	m = sub8Progress(t, m, sub8Explorer, "read_file", "payments/charge.go")
	clock.advance(25 * time.Second)
	m = sub8Usage(t, m, sub8Explorer, 5000, 1000)
	m = sub8Usage(t, m, sub8Explorer, 9000, 3000)
	return m
}

func sub8FrameModel(t *testing.T) (model, *sub8Clock) {
	t.Helper()
	m, clock := sub8Model(t)
	return sub8FrameTimeline(t, m, clock), clock
}

// sub8ThreeRunning has explorer, reviewer and planner running, each row keyed
// by its tool-call id as in a live run.
func sub8ThreeRunning(t *testing.T) model {
	t.Helper()
	m, clock := sub8Model(t)
	for _, child := range sub8Children {
		m = sub8Start(t, m, clock, child, false)
	}
	return m
}

func sub8CompactLine(view string) (string, bool) {
	for _, line := range strings.Split(view, "\n") {
		if sub8CompactRe.MatchString(line) {
			return strings.TrimSpace(line), true
		}
	}
	return "", false
}

func sub8Expanded(view string) bool { return strings.Contains(view, sub8ExpandedHdr) }

func sub8Line(view, needle string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// sub8PanelLines returns the expanded panel's row lines, in screen order.
func sub8PanelLines(t *testing.T, view string) []string {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if !strings.Contains(line, sub8ExpandedHdr) {
			continue
		}
		var rows []string
		for _, row := range lines[i+1:] {
			if strings.Contains(row, sub8Footer) || strings.Contains(row, "s/n") {
				break
			}
			rows = append(rows, row)
		}
		return rows
	}
	t.Fatalf("painel ampliado ausente; View=%q", view)
	return nil
}

func sub8PanelRow(t *testing.T, view, name string) string {
	t.Helper()
	for _, row := range sub8PanelLines(t, view) {
		if strings.Contains(row, name) {
			return row
		}
	}
	t.Fatalf("painel ampliado sem linha para %q; View=%q", name, view)
	return ""
}

// sub8PanelOrder lists the known children in the order the expanded panel
// shows them; ↑↓ and ←→ walk this order.
func sub8PanelOrder(t *testing.T, view string) []sub8Child {
	t.Helper()
	var order []sub8Child
	for _, row := range sub8PanelLines(t, view) {
		for _, child := range sub8Children {
			if strings.Contains(row, child.name) {
				order = append(order, child)
			}
		}
	}
	if len(order) != len(sub8Children) {
		t.Fatalf("o painel ampliado deve listar explorer, reviewer e planner uma vez cada; View=%q", view)
	}
	return order
}

// sub8Select is the user path to a row: Ctrl+G, then ↓ until the child's
// row, starting from the first row.
func sub8Select(t *testing.T, m model, child sub8Child) model {
	t.Helper()
	if !sub8Expanded(sub8View(m)) {
		m = sub8Update(t, m, sub8Ctrl('g'))
	}
	view := sub8View(m)
	if !sub8Expanded(view) {
		t.Fatalf("Ctrl+G não amplia o painel; View=%q", view)
	}
	for index, row := range sub8PanelOrder(t, view) {
		if row.name == child.name {
			for i := 0; i < index; i++ {
				m = sub8Update(t, m, sub8Key(tea.KeyDown))
			}
			return m
		}
	}
	t.Fatalf("painel sem a linha de %s; View=%q", child.name, view)
	return m
}

func sub8Open(t *testing.T, m model, child sub8Child) model {
	t.Helper()
	return sub8Update(t, sub8Select(t, m, child), sub8Key(tea.KeyEnter))
}

func sub8Want(t *testing.T, where, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("esperava %q em %s; got=%q", want, where, got)
		}
	}
}

func sub8WantRe(t *testing.T, where, got, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(got) {
		t.Fatalf("esperava /%s/ em %s; got=%q", pattern, where, got)
	}
}

func sub8Paging(view string) (int, int, bool) {
	match := sub8PagingRe.FindStringSubmatch(view)
	if match == nil {
		return 0, 0, false
	}
	var index, total int
	for _, r := range match[1] {
		index = index*10 + int(r-'0')
	}
	for _, r := range match[2] {
		total = total*10 + int(r-'0')
	}
	return index, total, true
}

// sub8WantConversation checks that the open conversation is the child's own:
// its transcript, its header and its position among the visible subagents.
func sub8WantConversation(t *testing.T, view string, child sub8Child, position int) {
	t.Helper()
	if !strings.Contains(view, child.marker) {
		t.Fatalf("a conversa aberta deve ser a de %s (mensagem %q); View=%q", child.name, child.marker, view)
	}
	if header := sub8Line(view, "qg ›"); !strings.Contains(header, "qg › ● "+child.name+" · "+child.desc) {
		t.Fatalf("o cabeçalho deve nomear o subagente aberto (%s · %s); got %q", child.name, child.desc, header)
	}
	if index, total, ok := sub8Paging(view); !ok || index != position || total != 3 {
		t.Fatalf("a posição de %s deve ser '%d de 3'; got %d de %d (ok=%v)", child.name, position, index, total, ok)
	}
}

// --- Compacto (quadro Discreto) ---------------------------------------------

func TestAcceptSubagents_CompactLineMatchesDiscretoFrame(t *testing.T) {
	m, _ := sub8FrameModel(t)
	view := sub8View(m)
	line, ok := sub8CompactLine(view)
	if !ok {
		t.Fatalf("linha compacta ausente com 3 subagentes visíveis; View=%q", view)
	}
	sub8WantRe(t, "linha compacta", line, `● 3 subagentes · 1 rodando · ⏱ 1m37s · 23,3k tok · `+sub8Money)
	sub8Want(t, "linha compacta", line, sub8Hint)
	if strings.Contains(line, "Ctrl+T") {
		t.Fatalf("Ctrl+T é do cycleReasoning; a dica deve citar Ctrl+G; linha=%q", line)
	}
	if strings.Contains(strings.ReplaceAll(view, "~R$", ""), "R$") || strings.Contains(view, "US$") {
		t.Fatalf("custo é sempre estimativa '~R$', nunca 'R$' puro nem US$; View=%q", view)
	}
}

func TestAcceptSubagents_CompactLineSitsBetweenComposerAndStatusline(t *testing.T) {
	m, _ := sub8FrameModel(t)
	lines := strings.Split(sub8View(m), "\n")
	prompt, compact, status := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "descreva uma tarefa para o qg"):
			prompt = i
		case sub8CompactRe.MatchString(line):
			compact = i
		case strings.Contains(line, "automático ·"):
			status = i
		}
	}
	if compact < 0 {
		t.Fatalf("linha compacta ausente; View=%q", strings.Join(lines, "\n"))
	}
	if prompt < 0 || status < 0 {
		t.Fatalf("fixture: composer ou statusline não encontrados; View=%q", strings.Join(lines, "\n"))
	}
	if compact != prompt+2 || compact != status-1 {
		t.Fatalf("a linha compacta deve ficar logo abaixo da borda do composer e logo acima da statusline (composer=%d compacta=%d status=%d)", prompt, compact, status)
	}
}

func TestAcceptSubagents_CompactLineHiddenWithoutSubagents(t *testing.T) {
	m, _ := sub8Model(t)
	if line, ok := sub8CompactLine(sub8View(m)); ok {
		t.Fatalf("sem subagentes não há linha compacta; got %q", line)
	}
}

func TestAcceptSubagents_CompactLineRetainedUnder30sAfterFinish(t *testing.T) {
	m, clock := sub8Model(t)
	m = sub8Start(t, m, clock, sub8Reviewer, false)
	clock.advance(20 * time.Second)
	m = sub8Complete(t, m, sub8Reviewer, specialistCompleted, "")

	clock.advance(29 * time.Second)
	if _, ok := sub8CompactLine(sub8View(m)); !ok {
		t.Fatalf("terminado há 29s ainda deve manter a linha compacta; View=%q", sub8View(m))
	}
	clock.advance(2 * time.Second)
	if line, ok := sub8CompactLine(sub8View(m)); ok {
		t.Fatalf("terminado há 31s a linha compacta deve sumir; got %q", line)
	}
}

func TestAcceptSubagents_CompactTotalsIgnoreExpiredSubagents(t *testing.T) {
	m, clock := sub8Model(t)
	old := sub8Child{"call-old", sub8Planner.session, "antigo", "terminou faz tempo", sub8Planner.marker}
	m = sub8Start(t, m, clock, old, false)
	clock.advance(10 * time.Second)
	sub8Rollup(t, m, old, 40000, 10000)
	m = sub8Complete(t, m, old, specialistCompleted, "")
	clock.advance(40 * time.Second)
	m = sub8Start(t, m, clock, sub8Reviewer, false)
	clock.advance(5 * time.Second)
	m = sub8Start(t, m, clock, sub8Explorer, false)
	clock.advance(10 * time.Second)
	m = sub8Usage(t, m, sub8Reviewer, 800, 200)
	m = sub8Usage(t, m, sub8Explorer, 1000, 500)

	line, ok := sub8CompactLine(sub8View(m))
	if !ok {
		t.Fatalf("linha compacta ausente com 2 subagentes rodando; View=%q", sub8View(m))
	}
	sub8WantRe(t, "linha compacta", line, `● 2 subagentes · 2 rodando · ⏱ 15s · 2,5k tok · `+sub8Money)
}

func TestAcceptSubagents_CompactClockIsLive(t *testing.T) {
	m, clock := sub8FrameModel(t)
	if line, ok := sub8CompactLine(sub8View(m)); !ok || !strings.Contains(line, "⏱ 1m37s") {
		t.Fatalf("relógio da linha compacta ausente; linha=%q", line)
	}
	clock.advance(3 * time.Second)
	if line, _ := sub8CompactLine(sub8View(m)); !strings.Contains(line, "⏱ 1m40s") {
		t.Fatalf("o relógio deve andar com o tempo (1m40s após +3s); linha=%q", line)
	}
}

func TestAcceptSubagents_StartEventAloneShowsCompactLineInSingular(t *testing.T) {
	m, clock := sub8Model(t)
	m = sub8Start(t, m, clock, sub8Explorer, false)
	clock.advance(12 * time.Second)

	line, ok := sub8CompactLine(sub8View(m))
	if !ok {
		t.Fatalf("specialistStartMsg deve acender a linha compacta; View=%q", sub8View(m))
	}
	sub8Want(t, "linha compacta", line, "● 1 subagente · 1 rodando · ⏱ 12s")
}

// --- Uso ao vivo --------------------------------------------------------------

func TestAcceptSubagents_UsageMessagesCarryRunningTotals(t *testing.T) {
	m, clock := sub8Model(t)
	m = sub8Start(t, m, clock, sub8Explorer, false)
	clock.advance(5 * time.Second)

	m = sub8Usage(t, m, sub8Explorer, 5000, 1000)
	m = sub8Update(t, m, sub8Ctrl('g'))
	row := sub8PanelRow(t, sub8View(m), "explorer")
	sub8Want(t, "linha explorer rodando", row, "rodando", "6,0k tok")
	sub8WantRe(t, "linha explorer rodando", row, sub8Money)

	m = sub8Usage(t, m, sub8Explorer, 9000, 3000)
	view := sub8View(m)
	row = sub8PanelRow(t, view, "explorer")
	if !strings.Contains(row, "12,0k tok") {
		t.Fatalf("specialistUsageMsg traz o total acumulado da chamada; 6,0k e depois 12,0k mostram 12,0k (não 18,0k); linha=%q", row)
	}
	sub8WantRe(t, "painel", view, `TOTAL ⏱ 5s · 12,0k tok · `+sub8Money)
}

func TestAcceptSubagents_CompletionUsesTheChildRollup(t *testing.T) {
	m, clock := sub8Model(t)
	m = sub8Start(t, m, clock, sub8Reviewer, false)
	m = sub8Usage(t, m, sub8Reviewer, 4000, 1000)
	clock.advance(20 * time.Second)
	sub8Rollup(t, m, sub8Reviewer, 6000, 2100)
	m = sub8Complete(t, m, sub8Reviewer, specialistCompleted, "")

	m = sub8Update(t, m, sub8Ctrl('g'))
	row := sub8PanelRow(t, sub8View(m), "reviewer")
	sub8Want(t, "linha reviewer", row, "✓", "pronto", "⏱ 20s")
	if !strings.Contains(row, "8,1k tok") {
		t.Fatalf("ao terminar vale o rollup do filho (8,1k), que substitui o uso ao vivo (5,0k) sem somar; linha=%q", row)
	}
	sub8WantRe(t, "linha reviewer", row, sub8Money)
}

// --- Ampliar e recolher ------------------------------------------------------

func TestAcceptSubagents_CtrlGExpandsAndCollapses(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Ctrl('g'))
	if view := sub8View(m); !sub8Expanded(view) {
		t.Fatalf("Ctrl+G deve ampliar o painel; View=%q", view)
	}
	m = sub8Update(t, m, sub8Ctrl('g'))
	view := sub8View(m)
	if sub8Expanded(view) {
		t.Fatalf("o segundo Ctrl+G deve recolher; View=%q", view)
	}
	if line, ok := sub8CompactLine(view); !ok || !strings.Contains(line, sub8Hint) {
		t.Fatalf("ao recolher volta a linha compacta com a dica; linha=%q", line)
	}
}

func TestAcceptSubagents_EscCollapsesExpandedPanel(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Ctrl('g'))
	if !sub8Expanded(sub8View(m)) {
		t.Fatalf("Ctrl+G deve ampliar o painel; View=%q", sub8View(m))
	}
	m = sub8Update(t, m, sub8Key(tea.KeyEsc))
	if sub8Expanded(sub8View(m)) {
		t.Fatalf("Esc deve recolher o painel; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_CtrlTStillCyclesReasoningWithSubagentsVisible(t *testing.T) {
	m, _ := sub8FrameModel(t)
	before := m.effortDisplay()
	m = sub8Update(t, m, sub8Ctrl('t'))
	if after := m.effortDisplay(); after == before {
		t.Fatalf("Ctrl+T deve continuar alternando o raciocínio (%q → %q)", before, after)
	}
	if sub8Expanded(sub8View(m)) {
		t.Fatalf("Ctrl+T não pode abrir o painel de subagentes; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_ClickOnCompactLineExpands(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m.altScreen = true
	lines := strings.Split(sub8View(m), "\n")
	y := -1
	for i, line := range lines {
		if sub8CompactRe.MatchString(line) {
			y = i
			break
		}
	}
	if y < 0 {
		t.Fatalf("não há linha compacta para clicar; View=%q", strings.Join(lines, "\n"))
	}
	x := strings.Index(lines[y], "●")
	m = sub8Update(t, m, tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: len([]rune(lines[y][:x])) + 2, Y: y}))
	if !sub8Expanded(sub8View(m)) {
		t.Fatalf("clique na linha compacta deve ampliar; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_DownExpandsWhenComposerEmpty(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Key(tea.KeyDown))
	if !sub8Expanded(sub8View(m)) {
		t.Fatalf("↓ com composer vazio deve ampliar; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_DownExpandsWhenCursorOnLastLine(t *testing.T) {
	m, _ := sub8FrameModel(t)
	const draft = "primeira linha\nsegunda linha"
	m.setComposerState(composerState{text: draft, cursor: len([]rune(draft))})
	m = sub8Update(t, m, sub8Key(tea.KeyDown))
	if !sub8Expanded(sub8View(m)) {
		t.Fatalf("↓ com o cursor na última linha deve ampliar; View=%q", sub8View(m))
	}
	if got := m.composerValue(); got != draft {
		t.Fatalf("ampliar não pode mexer no rascunho; got %q", got)
	}
}

func TestAcceptSubagents_DownNotStolenOnMultilineComposer(t *testing.T) {
	m, _ := sub8FrameModel(t)
	const draft = "primeira linha\nsegunda linha"
	m.setComposerState(composerState{text: draft, cursor: 3})
	before := m.currentComposerState().cursor

	m = sub8Update(t, m, sub8Key(tea.KeyDown))
	if got := m.composerValue(); got != draft {
		t.Fatalf("↓ não pode alterar o rascunho; got %q", got)
	}
	if m.currentComposerState().cursor == before {
		t.Fatal("↓ deve mover o cursor dentro do composer multilinha")
	}
	if sub8Expanded(sub8View(m)) {
		t.Fatalf("↓ fora da última linha não pode ampliar; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_DownStillStepsHistoryWhileRecalling(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m.inputHistory = []string{"antiga", "recente"}
	m.historyIdx = len(m.inputHistory)

	m = sub8Update(t, m, sub8Key(tea.KeyUp))
	m = sub8Update(t, m, sub8Key(tea.KeyUp))
	if got := m.composerValue(); got != "antiga" {
		t.Fatalf("fixture: ↑↑ deveria recuperar 'antiga', got %q", got)
	}
	m = sub8Update(t, m, sub8Key(tea.KeyDown))
	if got := m.composerValue(); got != "recente" {
		t.Fatalf("durante a navegação no histórico ↓ volta para a entrada mais nova; got %q", got)
	}
	if sub8Expanded(sub8View(m)) {
		t.Fatalf("↓ no histórico não pode ampliar o painel; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_UpStillEditsLastQueuedMessage(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m.queuedMessages = []string{"primeira", "ultima"}
	m = sub8Update(t, m, sub8Key(tea.KeyUp))
	if got := m.composerValue(); got != "ultima" {
		t.Fatalf("↑ continua editando a última mensagem da fila; got %q", got)
	}
}

// --- Ampliado ----------------------------------------------------------------

func TestAcceptSubagents_ExpandedHeaderAndTotal(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Ctrl('g'))
	view := sub8View(m)
	if !sub8Expanded(view) {
		t.Fatalf("Ctrl+G deve ampliar o painel; View=%q", view)
	}
	sub8Want(t, "painel", view, "SUBAGENTES · 1 RODANDO · 1 PRONTO · 1 COM ERRO")
	sub8WantRe(t, "painel", view, `TOTAL ⏱ 1m37s · 23,3k tok · `+sub8Money)
}

func TestAcceptSubagents_ExpandedRowPerSubagent(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Ctrl('g'))
	view := sub8View(m)

	explorer := sub8PanelRow(t, view, "explorer")
	sub8Want(t, "linha explorer", explorer, "●", "↳", "payments/charge.go", "rodando", "⏱ 25s", "12,0k tok")
	sub8WantRe(t, "linha explorer", explorer, sub8Money)

	reviewer := sub8PanelRow(t, view, "reviewer")
	sub8Want(t, "linha reviewer", reviewer, "✓", "pronto", "⏱ 1m12s", "8,1k tok")
	sub8WantRe(t, "linha reviewer", reviewer, sub8Money)

	planner := sub8PanelRow(t, view, "planner")
	sub8Want(t, "linha planner", planner, "✗", "tempo esgotado no provedor", "falhou", "⏱ 40s", "3,2k tok")
	sub8WantRe(t, "linha planner", planner, sub8Money)
}

func TestAcceptSubagents_ExpandedFooterExactText(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Update(t, m, sub8Ctrl('g'))
	if view := sub8View(m); !strings.Contains(view, sub8Footer) {
		t.Fatalf("rodapé do painel difere do quadro Ampliado (com Ctrl+G); View=%q", view)
	}
}

func TestAcceptSubagents_ForegroundAndBackgroundShareOneList(t *testing.T) {
	m, clock := sub8Model(t)
	foreground := sub8Child{"call-fg", sub8Explorer.session, "primeiro-plano", "em primeiro plano", sub8Explorer.marker}
	background := sub8Child{"call-bg", sub8Reviewer.session, "segundo-plano", "em segundo plano", sub8Reviewer.marker}
	m = sub8Start(t, m, clock, foreground, false)
	m = sub8Start(t, m, clock, background, true)
	// A background Task returns at once: that result is the spawn ack.
	m = sub8Complete(t, m, background, specialistCompleted, "")
	clock.advance(5 * time.Second)
	m = sub8Update(t, m, sub8Ctrl('g'))

	view := sub8View(m)
	if strings.Count(view, sub8ExpandedHdr) != 1 {
		t.Fatalf("deve haver exatamente uma lista de subagentes; View=%q", view)
	}
	sub8Want(t, "linha primeiro-plano", sub8PanelRow(t, view, "primeiro-plano"), "rodando")
	sub8Want(t, "linha segundo-plano", sub8PanelRow(t, view, "segundo-plano"), "rodando")
	sub8Want(t, "painel", view, "SUBAGENTES · 2 RODANDO")
}

func TestAcceptSubagents_XAsksConfirmationBeforeStopping(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Select(t, m, sub8Explorer)
	m = sub8Update(t, m, sub8Rune('x'))
	view := sub8View(m)
	if line := sub8Line(view, "s/n"); !strings.Contains(line, "explorer") {
		t.Fatalf("x deve pedir confirmação (s/n) nomeando o subagente escolhido; View=%q", view)
	}
	if strings.Contains(m.composerValue(), "x") {
		t.Fatalf("x no painel não pode ir para o composer; composer=%q", m.composerValue())
	}
	m = sub8Update(t, m, sub8Rune('n'))
	view = sub8View(m)
	if strings.Contains(view, "s/n") {
		t.Fatalf("'n' deve fechar a confirmação; View=%q", view)
	}
	sub8Want(t, "linha explorer após 'n'", sub8PanelRow(t, view, "explorer"), "rodando")
}

func TestAcceptSubagents_NormalFinishAfterAStopRequestShowsPronto(t *testing.T) {
	m := sub8ThreeRunning(t)
	m = sub8Select(t, m, sub8Explorer)
	m = sub8Update(t, m, sub8Rune('x'))
	next, cmd := m.Update(sub8Rune('s'))
	m = next.(model)
	if cmd == nil {
		t.Fatal("'s' deve acionar a parada")
	}
	m = sub8Update(t, m, cmd())
	m = sub8Complete(t, m, sub8Explorer, specialistCompleted, "")
	if !sub8Expanded(sub8View(m)) {
		m = sub8Update(t, m, sub8Ctrl('g'))
	}
	row := sub8PanelRow(t, sub8View(m), "explorer")
	if strings.Contains(row, "parado") || !strings.Contains(row, "pronto") {
		t.Fatalf("a parada não encerrou o filho (aqui não há processo vivo) e ele terminou normalmente: a linha deve dizer 'pronto', não 'parado'; linha=%q", row)
	}
}

// --- Conversa do subagente (quadro ChatSub) ----------------------------------

func TestAcceptSubagents_EnterOpensEachRowsOwnConversation(t *testing.T) {
	order := sub8PanelOrder(t, sub8View(sub8Update(t, sub8ThreeRunning(t), sub8Ctrl('g'))))
	for index, child := range order {
		m := sub8Open(t, sub8ThreeRunning(t), child)
		sub8WantConversation(t, sub8View(m), child, index+1)
		if got := m.composerValue(); got != "" {
			t.Fatalf("Enter não pode enviar nem escrever no composer; composer=%q", got)
		}
	}
}

func TestAcceptSubagents_LeftRightPageThroughRealConversations(t *testing.T) {
	m := sub8ThreeRunning(t)
	order := sub8PanelOrder(t, sub8View(sub8Update(t, m, sub8Ctrl('g'))))
	m = sub8Open(t, m, order[0])
	sub8WantConversation(t, sub8View(m), order[0], 1)
	steps := []struct {
		key  rune
		want int
	}{{tea.KeyRight, 1}, {tea.KeyRight, 2}, {tea.KeyLeft, 1}, {tea.KeyLeft, 0}}
	for _, step := range steps {
		m = sub8Update(t, m, sub8Key(step.key))
		sub8WantConversation(t, sub8View(m), order[step.want], step.want+1)
	}
}

func TestAcceptSubagents_ConversationChromeMatchesChatSubFrame(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Open(t, m, sub8Explorer)
	view := sub8View(m)
	if !strings.Contains(view, sub8Explorer.marker) {
		t.Fatalf("Enter na linha do explorer deve abrir a conversa dele; View=%q", view)
	}
	sub8Want(t, "ChatSub", view, "qg › ● explorer · mapear o módulo de pagamentos", "só leitura", sub8ChatSubFooter)
	sub8WantRe(t, "ChatSub", view, `rodando · ⏱ 25s · 12,0k tok · `+sub8Money)
	sub8WantRe(t, "ChatSub", view, `\d de 3 · ←→ outro subagente`)
}

func TestAcceptSubagents_ConversationIsReadOnlyWhileRunning(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m = sub8Open(t, m, sub8Explorer)
	if !strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("Enter na linha do explorer deve abrir a conversa dele; View=%q", sub8View(m))
	}
	m = sub8Update(t, m, sub8Rune('a'))
	if got := m.composerValue(); got != "" {
		t.Fatalf("a conversa do subagente é só leitura enquanto ele roda; composer=%q", got)
	}
	if !strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("digitar na conversa só leitura não pode fechá-la; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_ConversationXTargetsTheOpenSubagent(t *testing.T) {
	m := sub8ThreeRunning(t)
	m = sub8Open(t, m, sub8Explorer)
	if !strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("Enter na linha do explorer deve abrir a conversa dele; View=%q", sub8View(m))
	}
	m = sub8Update(t, m, sub8Rune('x'))
	if line := sub8Line(sub8View(m), "s/n"); !strings.Contains(line, "explorer") {
		t.Fatalf("x na conversa deve pedir confirmação (s/n) para parar o subagente aberto (explorer); got %q", line)
	}
	next, cmd := m.Update(sub8Rune('s'))
	m = next.(model)
	if cmd == nil {
		t.Fatal("'s' deve acionar a parada")
	}
	m = sub8Update(t, m, cmd())
	if strings.Contains(sub8View(m), "já terminou") {
		t.Fatalf("o explorer está rodando; a parada não pode responder 'subagente já terminou'; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_ConversationFollowsTheRunningChild(t *testing.T) {
	m, clock := sub8Model(t)
	for _, child := range sub8Children {
		m = sub8Start(t, m, clock, child, false)
	}
	m = sub8Open(t, m, sub8Explorer)
	if !strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("Enter na linha do explorer deve abrir a conversa dele; View=%q", sub8View(m))
	}
	const fresh = "Os pontos de entrada são Service.Charge e Service.Refund."
	sub8Append(t, m.sessionStore, sub8Explorer.session, sessions.EventMessage, map[string]any{"role": "assistant", "content": fresh})
	m.pending = true
	m = sub8Progress(t, m, sub8Explorer, "read_file", "payments/charge.go")
	// The contract is "within a second", so a 1x/s reload throttle stays legal.
	clock.advance(time.Second)
	m = sub8Update(t, m, spinner.TickMsg{})
	if !strings.Contains(sub8View(m), fresh) {
		t.Fatalf("a conversa aberta deve acompanhar o filho enquanto ele roda; View=%q", sub8View(m))
	}
}

func TestAcceptSubagents_EscReturnsFromConversation(t *testing.T) {
	m, _ := sub8FrameModel(t)
	m.chatScrollOffset = 7
	m = sub8Open(t, m, sub8Explorer)
	if !strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("Enter na linha do explorer deve abrir a conversa dele; View=%q", sub8View(m))
	}
	m = sub8Update(t, m, sub8Key(tea.KeyEsc))
	if strings.Contains(sub8View(m), sub8Explorer.marker) {
		t.Fatalf("Esc volta para a conversa principal; View=%q", sub8View(m))
	}
	if m.chatScrollOffset != 7 {
		t.Fatalf("Esc restaura o scroll da conversa principal; got %d", m.chatScrollOffset)
	}
}

func TestAcceptSubagents_OpenConversationKeepsItsChromeWhenTheChildFinishes(t *testing.T) {
	m := sub8ThreeRunning(t)
	order := sub8PanelOrder(t, sub8View(sub8Update(t, m, sub8Ctrl('g'))))
	m = sub8Open(t, m, order[0])
	sub8WantConversation(t, sub8View(m), order[0], 1)
	m = sub8Complete(t, m, order[0], specialistCompleted, "")
	view := sub8View(m)
	if !strings.Contains(view, order[0].marker) {
		t.Fatalf("a conversa aberta continua na tela quando o filho termina; View=%q", view)
	}
	if header := sub8Line(view, "qg ›"); !strings.Contains(header, "qg › ✓ "+order[0].name+" · "+order[0].desc) || !strings.Contains(header, "pronto") {
		t.Fatalf("quando o filho aberto termina, o cabeçalho do quadro ChatSub continua e passa a '✓ … pronto'; got %q (1ª linha %q)", header, strings.SplitN(view, "\n", 2)[0])
	}
	if index, total, ok := sub8Paging(view); !ok || index != 1 || total != 3 {
		t.Fatalf("a posição continua '1 de 3' quando o filho aberto termina; got %d de %d (ok=%v)", index, total, ok)
	}
}

func TestAcceptSubagents_RightOpensASiblingThatFinishedAfterTheDrillIn(t *testing.T) {
	m := sub8ThreeRunning(t)
	order := sub8PanelOrder(t, sub8View(sub8Update(t, m, sub8Ctrl('g'))))
	m = sub8Open(t, m, order[0])
	m = sub8Complete(t, m, order[1], specialistCompleted, "")
	m = sub8Update(t, m, sub8Key(tea.KeyRight))
	view := sub8View(m)
	if !strings.Contains(view, order[1].marker) {
		t.Fatalf("→ deve abrir a conversa de %s, que terminou depois da entrada e segue visível na retenção; View=%q", order[1].name, view)
	}
	if header := sub8Line(view, "qg ›"); !strings.Contains(header, "qg › ✓ "+order[1].name+" · "+order[1].desc) {
		t.Fatalf("o cabeçalho deve nomear %s, já pronto; got %q", order[1].name, header)
	}
	if index, total, ok := sub8Paging(view); !ok || index != 2 || total != 3 {
		t.Fatalf("a posição deve ser '2 de 3'; got %d de %d (ok=%v)", index, total, ok)
	}
}

// --- Relógio e parada ---------------------------------------------------------

func TestAcceptSubagents_TickKeepsRunningWhileSubagentsVisibleAfterTheTurn(t *testing.T) {
	m := sub8ThreeRunning(t)
	if _, ok := sub8CompactLine(sub8View(m)); !ok {
		t.Fatalf("fixture: linha compacta deveria estar visível; View=%q", sub8View(m))
	}
	if _, cmd := m.Update(spinner.TickMsg{}); cmd == nil {
		t.Fatal("com subagente visível e o turno encerrado o tick deve continuar (relógio ao vivo, retenção de 30s e reconciliação do background)")
	}
}

func TestAcceptSubagents_StopCommandDoesNotRaceTheUpdateLoop(t *testing.T) {
	m := sub8ThreeRunning(t)
	m = sub8Select(t, m, sub8Explorer)
	m = sub8Update(t, m, sub8Rune('x'))
	next, cmd := m.Update(sub8Rune('s'))
	m = next.(model)
	if cmd == nil {
		t.Fatal("'s' deve acionar a parada")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = cmd() }()
	for i := 0; i < 200; i++ {
		m = sub8Progress(t, m, sub8Explorer, "read_file", "a.go")
	}
	wg.Wait()
}

// --- Ajuda (?) ---------------------------------------------------------------

func sub8HelpFits(t *testing.T, view, when string) {
	t.Helper()
	if lines := len(strings.Split(strings.TrimSuffix(view, "\n"), "\n")); lines > 40 {
		t.Fatalf("a tela com a ajuda %s tem %d linhas num terminal de 40; View=%q", when, lines, view)
	}
	if !strings.Contains(view, sub8HelpHint) {
		t.Fatalf("a dica %q deve estar visível %s num terminal de 40 linhas (ajuda compacta ou rolagem com a dica fixa); View=%q", sub8HelpHint, when, view)
	}
}

// sub8HelpShowsCtrlG looks for the Ctrl+G row; the fixture has no subagents,
// so the compact line (which also cites Ctrl+G) is not on screen.
func sub8HelpShowsCtrlG(view string) bool {
	return strings.Contains(view, "Ctrl+G")
}

func TestAcceptSubagents_HelpFitsFortyRowsWithCloseHintVisible(t *testing.T) {
	m, _ := sub8Model(t)
	m.altScreen = true
	m = sub8Update(t, m, sub8Rune('?'))
	view := sub8View(m)
	if !strings.Contains(view, "Atalhos de teclado") {
		t.Fatalf("fixture: '?' com o composer vazio deveria abrir a ajuda; View=%q", view)
	}
	sub8HelpFits(t, view, "ao abrir")
	reached := sub8HelpShowsCtrlG(view)
	for _, scroll := range []struct {
		key  rune
		name string
	}{{tea.KeyPgDown, "PgDn"}, {tea.KeyDown, "↓"}} {
		scrolled := m
		for step := 1; !reached && step <= 40; step++ {
			scrolled = sub8Update(t, scrolled, sub8Key(scroll.key))
			view = sub8View(scrolled)
			sub8HelpFits(t, view, fmt.Sprintf("após %d× %s", step, scroll.name))
			reached = sub8HelpShowsCtrlG(view)
		}
	}
	if !reached {
		t.Fatalf("o atalho Ctrl+G dos subagentes deve estar na ajuda ou ao alcance de PgDn/↓; View=%q", view)
	}
}

func TestAcceptSubagents_HelpStillExplainsEscAndTheMouseToggle(t *testing.T) {
	m, _ := sub8Model(t)
	m.altScreen = true
	m.keyBindings.toggleMouse = parseBinding("ctrl+y")
	m = sub8Update(t, m, sub8Rune('?'))
	seen := strings.Split(sub8View(m), "\n")
	scrolled := m
	for step := 0; step < 40; step++ {
		scrolled = sub8Update(t, scrolled, sub8Key(tea.KeyPgDown))
		seen = append(seen, strings.Split(sub8View(scrolled), "\n")...)
	}
	wants := []struct{ key, desc string }{
		{"Esc", "cancela a execução"},
		{"Ctrl+Y", "mouse"},
	}
	for _, want := range wants {
		found := false
		for _, line := range seen {
			if strings.Contains(line, want.key) && strings.Contains(line, want.desc) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("compactar a ajuda não pode apagar atalhos: falta a linha de %s (%q), com o rótulo configurado pelo usuário; View=%q", want.key, want.desc, sub8View(m))
		}
	}
}

// --- Fora de escopo ----------------------------------------------------------

func TestAcceptSubagents_HeadlessTasksCommandStaysOutOfScope(t *testing.T) {
	for _, name := range listCommandNames() {
		if strings.EqualFold(strings.TrimPrefix(name, "/"), "tasks") {
			t.Fatalf("o comando headless 'tasks' está fora do escopo do painel de subagentes; got %q", name)
		}
	}
}

func TestAcceptSubagents_TickThrottlesReloadToOnePerSecond(t *testing.T) {
	m, clock := sub8Model(t)
	for _, child := range sub8Children {
		m = sub8Start(t, m, clock, child, false)
	}
	m = sub8Open(t, m, sub8Explorer)
	m.pending = true
	m = sub8Update(t, m, spinner.TickMsg{})
	const fresh = "linha nova sem progresso no tick"
	sub8Append(t, m.sessionStore, sub8Explorer.session, sessions.EventMessage, map[string]any{"role": "assistant", "content": fresh})
	clock.advance(500 * time.Millisecond)
	m = sub8Update(t, m, spinner.TickMsg{})
	if strings.Contains(sub8View(m), fresh) {
		t.Fatalf("tick a +0,5s deve esconder a linha nova (limite de 1x/s); View=%q", sub8View(m))
	}
	clock.advance(600 * time.Millisecond)
	m = sub8Update(t, m, spinner.TickMsg{})
	if !strings.Contains(sub8View(m), fresh) {
		t.Fatalf("tick a +1,1s deve mostrar a linha nova; View=%q", sub8View(m))
	}
}
