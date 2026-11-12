package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/background"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/streamjson"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func panelTestModel(at time.Time) model {
	return model{now: func() time.Time { return at }}
}

func TestSubagentsPanelCompactLine(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	if line := plainRender(t, m.subagentsCompactLine(100)); line != "" {
		t.Fatalf("empty panel must render nothing, got %q", line)
	}

	m.subagents.upsertStart("c1", "explorer", "mapear", false, false, 1, base.Add(-97*time.Second))
	m.subagents.setUsage("c1", 22000, 1300, "", 0, false)
	line := plainRender(t, m.subagentsCompactLine(100))
	for _, want := range []string{"1 subagente", "1 rodando", "1m37s", "23,3k tok"} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line must contain %q, got %q", want, line)
		}
	}
	if strings.Contains(line, "Ctrl+T") || !strings.Contains(line, "Ctrl+G") {
		t.Fatalf("compact hint must name Ctrl+G, got %q", line)
	}
}

func TestSubagentsPanelTotalsAcrossRows(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, base.Add(-25*time.Second))
	m.subagents.upsertStart("c2", "reviewer", "", false, false, 1, base.Add(-72*time.Second))
	m.subagents.setUsage("c1", 11500, 500, "", 0.002, true)
	m.subagents.setUsage("c2", 8000, 100, "", 0.001, true)
	m.subagents.completeToolCall("c2", "", true, "", base.Add(-10*time.Second))

	line := plainRender(t, m.subagentsCompactLine(120))
	for _, want := range []string{"2 subagentes", "1 rodando", "1m12s", "20,1k tok", "~R$ 0,003"} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact totals must contain %q, got %q", line, line)
		}
	}
}

func TestSubagentsPanelRetentionDropsAfter30s(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, base.Add(-60*time.Second))
	m.subagents.completeToolCall("c1", "", true, "", base.Add(-29*time.Second))
	if got := len(m.subagents.visibleRows(base)); got != 1 {
		t.Fatalf("finished 29s ago must stay visible, got %d rows", got)
	}
	m.subagents.prune(base.Add(2 * time.Second))
	if got := len(m.subagents.visibleRows(base.Add(2 * time.Second))); got != 0 {
		t.Fatalf("finished 31s ago must drop, got %d rows", got)
	}
	if line := plainRender(t, m.subagentsCompactLine(100)); line != "" {
		t.Fatalf("panel must hide after retention, got %q", line)
	}
}

func TestSubagentsPanelBackgroundSpawnAckStaysRunning(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("c1", "explorer", "", false, true, 1, base)
	m.subagents.completeToolCall("c1", "", true, "", base)
	rows := m.subagents.visibleRows(base)
	if len(rows) != 1 || rows[0].state != subagentRunning {
		t.Fatalf("background spawn ack must not complete the row: %#v", rows)
	}
}

func TestSubagentsExpandedShowsHeaderRowsFooter(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.width = 100
	m.subagents.upsertStart("c1", "explorer", "mapear", false, false, 1, base.Add(-25*time.Second))
	m.subagents.setActivity("c1", "↳ lendo payments/charge.go")
	m.subagents.upsertStart("c2", "reviewer", "", false, false, 1, base.Add(-82*time.Second))
	m.subagents.setUsage("c2", 8000, 100, "", 0.001, true)
	m.subagents.completeToolCall("c2", "sess-2", true, "", base.Add(-10*time.Second))
	m.subagents.upsertStart("c3", "planner", "", false, false, 1, base.Add(-40*time.Second))
	m.subagents.completeToolCall("c3", "", false, "tempo esgotado no provedor", base.Add(-29*time.Second))
	m.subagents.expanded = true

	out := plainRender(t, m.renderSubagentsExpanded(130, base))
	for _, want := range []string{
		"SUBAGENTES", "1 RODANDO", "1 PRONTO", "1 COM ERRO",
		"explorer", "↳ lendo payments/charge.go", "rodando", "25s",
		"reviewer", "pronto", "1m12s", "8,1k tok", "~R$ 0,001",
		"planner", "tempo esgotado no provedor", "falhou",
		"↑↓ escolhe", "Enter abre a conversa", "x para", "Ctrl+G recolhe",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expanded panel must contain %q, got:\n%s", want, out)
		}
	}
}

func TestSubagentsExpandedSelectionMovesAndClamps(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("c1", "a", "", false, false, 1, base)
	m.subagents.upsertStart("c2", "b", "", false, false, 1, base)
	m.subagents.selected = 5
	m.subagents.prune(base)
	if m.subagents.selected != 1 {
		t.Fatalf("selection must clamp to last row, got %d", m.subagents.selected)
	}
}

func TestSubagentsExpandedConfirmStopFlow(t *testing.T) {
	m := panelTestModel(time.Now())
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, time.Now())
	m.subagents.expanded = true
	m.subagents.selected = 0
	m.subagents.confirmStop = true
	m.subagents.confirmKey = "c1"
	out := plainRender(t, m.renderSubagentsExpanded(130, time.Now()))
	if !strings.Contains(out, `parar "explorer"? (s/n)`) {
		t.Fatalf("confirm prompt must name the agent, got:\n%s", out)
	}
}

func TestSubagentsKeysToggleExpandAndSelect(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.width = 100
	m.subagents.upsertStart("c1", "a", "", false, false, 1, base)
	m.subagents.upsertStart("c2", "b", "", false, false, 1, base)

	updated, _ := m.Update(testKey(tea.KeyDown))
	m = updated.(model)
	if !m.subagents.expanded {
		t.Fatal("Down on empty composer must expand the panel")
	}
	updated, _ = m.Update(testKey(tea.KeyDown))
	m = updated.(model)
	if m.subagents.selected != 1 {
		t.Fatalf("Down in expanded panel must move selection, got %d", m.subagents.selected)
	}
	updated, _ = m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if m.subagents.selected != 0 {
		t.Fatalf("Up in expanded panel must move selection back, got %d", m.subagents.selected)
	}
	updated, _ = m.Update(testKeyCtrl('g'))
	m = updated.(model)
	if m.subagents.expanded {
		t.Fatal("Ctrl+G must collapse the expanded panel")
	}
}

func TestSubagentsKeysConfirmStopFlow(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.width = 100
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, base)
	m.subagents.expanded = true

	updated, _ := m.Update(testKeyText("x"))
	m = updated.(model)
	if !m.subagents.confirmStop || m.subagents.confirmKey != "c1" {
		t.Fatal("x must open the stop confirmation for the selected row")
	}
	updated, _ = m.Update(testKey(tea.KeyEsc))
	m = updated.(model)
	if m.subagents.confirmStop {
		t.Fatal("Esc must cancel the stop confirmation")
	}
}

func subchatPanelTestModel(t *testing.T, base time.Time) model {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	m := newModel(context.Background(), Options{ModelName: "gpt-4", SessionStore: store})
	m.width = 100
	m.height = 40
	m.altScreen = true
	m.now = func() time.Time { return base }
	for _, id := range []string{"sess-1", "sess-2", "sess-3"} {
		if _, err := store.Create(sessions.CreateInput{SessionID: id}); err != nil {
			t.Fatalf("create child session: %v", err)
		}
	}
	return m
}

func TestSubagentsEnterOpensSubchat(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	m.subagents.upsertStart("c1", "explorer", "mapear", false, false, 1, base)
	m.subagents.completeToolCall("c1", "sess-1", true, "", base)
	m.subagents.expanded = true
	m.subagents.selected = 0

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !m.subchat.active || m.subchat.childSessionID != "sess-1" {
		t.Fatalf("Enter must drill into the selected subagent, active=%v child=%q", m.subchat.active, m.subchat.childSessionID)
	}
}

func TestSubagentsSubchatCyclesWithArrows(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	now := base
	m.subagents.upsertStart("c1", "a", "", false, false, 1, now)
	m.subagents.completeToolCall("c1", "sess-1", true, "", now)
	m.subagents.upsertStart("c2", "b", "", false, false, 1, now)
	m.subagents.completeToolCall("c2", "sess-2", true, "", now)
	m.subagents.expanded = true
	m.subagents.selected = 0

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	updated, _ = m.Update(testKey(tea.KeyRight))
	m = updated.(model)
	if m.subchat.childSessionID != "sess-2" {
		t.Fatalf("→ must move to the next subagent, got %q", m.subchat.childSessionID)
	}
	updated, _ = m.Update(testKey(tea.KeyLeft))
	m = updated.(model)
	if m.subchat.childSessionID != "sess-1" {
		t.Fatalf("← must move back, got %q", m.subchat.childSessionID)
	}
}

func TestSubagentsCompactClickExpands(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := newModel(context.Background(), Options{})
	m.now = func() time.Time { return base }
	m.width = 100
	m.height = 30
	m.altScreen = true
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, base)

	footer := strings.Split(m.footerView(100), "\n")
	compactIdx := 1 + len(viewLines(m.composerBox(m.chatColumnWidth())))
	screenY := m.height - len(footer) + compactIdx
	updated, _ := m.handleMouse(testMouseClick(tea.MouseLeft, 10, screenY))
	mm := updated.(model)
	if !mm.subagents.expanded {
		t.Fatalf("click on the compact line (y=%d) must expand the panel", screenY)
	}
	// A click elsewhere must not expand.
	m2 := newModel(context.Background(), Options{})
	m2.now = func() time.Time { return base }
	m2.width = 100
	m2.height = 30
	m2.altScreen = true
	m2.subagents.upsertStart("c1", "explorer", "", false, false, 1, base)
	updated2, _ := m2.handleMouse(testMouseClick(tea.MouseLeft, 10, 2))
	if updated2.(model).subagents.expanded {
		t.Fatal("a transcript click must not expand the panel")
	}
}

func TestSubagentsEnterOpensConversation(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	m.subagents.upsertStart("c1", "explorer", "mapear", false, false, 1, base)
	m.subagents.completeToolCall("c1", "sess-1", true, "", base)
	m.subagents.expanded = true
	m.subagents.selected = 0

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !m.subchat.active || m.subchat.childSessionID != "sess-1" {
		t.Fatalf("Enter must open the selected subagent, active=%v child=%q", m.subchat.active, m.subchat.childSessionID)
	}
	if got := m.composerValue(); got != "" {
		t.Fatalf("Enter must not write to the composer, got %q", got)
	}
}

func TestSubagentsReadOnlyWhileRunning(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	m.subagents.upsertStart("c1", "explorer", "", false, false, 1, base)
	m.subagents.expanded = true
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !m.subchat.active {
		t.Fatal("Enter must open the running subagent")
	}
	updated, _ = m.Update(testKeyText("a"))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("typing in a running subagent conversation must not write, got %q", got)
	}
	if !m.subchat.active {
		t.Fatal("typing must not close the subchat")
	}
}

type subagentStopFake struct {
	killed []string
}

func (fake *subagentStopFake) Name() string { return "TaskStop" }
func (fake *subagentStopFake) Description() string {
	return "test stop"
}
func (fake *subagentStopFake) Parameters() tools.Schema { return tools.Schema{Type: "object"} }
func (fake *subagentStopFake) Safety() tools.Safety {
	return tools.Safety{Permission: tools.PermissionAllow, AdvertiseInAuto: true}
}
func (fake *subagentStopFake) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK}
}
func (fake *subagentStopFake) KillTask(taskID string) error {
	fake.killed = append(fake.killed, taskID)
	return nil
}
func (fake *subagentStopFake) BackgroundTask(taskID string) (background.Task, bool) {
	return background.Task{}, false
}

func TestSubagentsStopConfirmsAndKills(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.width = 100
	reg := tools.NewRegistry()
	stopper := &subagentStopFake{}
	reg.Register(stopper)
	m.registry = reg
	m.subagents.upsertStart("sess-9", "explorer", "", false, true, 1, base)

	m.subagents.expanded = true
	updated, _ := m.Update(testKeyText("x"))
	m = updated.(model)
	if !m.subagents.confirmStop {
		t.Fatal("x must open the stop confirmation")
	}
	updated, cmd := m.Update(testKeyText("s"))
	m = updated.(model)
	if m.subagents.confirmStop {
		t.Fatal("s must resolve the confirmation")
	}
	if cmd == nil {
		t.Fatal("s must produce a stop command")
	}
	if msg, ok := cmd().(subagentStopDoneMsg); !ok || msg.err != nil {
		t.Fatalf("stop command must succeed, got %#v", msg)
	}
	if len(stopper.killed) != 1 || stopper.killed[0] != "sess-9" {
		t.Fatalf("KillTask must run once for the session, got %#v", stopper.killed)
	}
}

func subagentsStoreWithRollup(t *testing.T, sessionID, childID string, prompt, completion int64, modelID, startedAt string) *sessions.Store {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	if _, err := store.Create(sessions.CreateInput{SessionID: sessionID}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.Create(sessions.CreateInput{SessionID: childID, ModelID: modelID}); err != nil {
		t.Fatalf("create child session: %v", err)
	}
	payload := map[string]any{
		"source": "specialist", "childSessionId": childID, "toolCallId": "call-1",
		"promptTokens": float64(prompt), "completionTokens": float64(completion),
	}
	if startedAt != "" {
		payload["startedAt"] = startedAt
	}
	if _, err := store.AppendEvent(sessionID, sessions.AppendEventInput{Type: sessions.EventSpecialistStart, Payload: payload}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	rollup := map[string]any{
		"source": "specialist", "childSessionId": childID,
		"promptTokens": float64(prompt), "completionTokens": float64(completion),
	}
	if _, err := store.AppendEvent(sessionID, sessions.AppendEventInput{Type: sessions.EventUsage, Payload: rollup}); err != nil {
		t.Fatalf("append usage: %v", err)
	}
	return store
}

func TestSubagentsRefreshUsageFromStore(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.activeSession = sessions.Metadata{SessionID: "parent-1"}
	m.sessionStore = subagentsStoreWithRollup(t, "parent-1", "child-1", 8000, 100, "modelo-teste", base.Add(-90*time.Second).Format(time.RFC3339Nano))
	m.modelName = "modelo-teste"
	m.providerName = "qg"
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"qg": {{
			ID: "modelo-teste", Source: "live",
			APIModel: &qgaccount.ModelInfo{
				ID:         "modelo-teste",
				PricingBRL: qgaccount.ModelPricing{Currency: "BRL", Input: "0.5", Output: "1"},
			},
		}},
	}

	row := subagentRow{key: "call-1", name: "reviewer", state: subagentRunning, startedAt: base}
	m.refreshSubagentUsage(&row)
	if !row.hasUsage || row.promptTokens+row.completionTokens != 8100 {
		t.Fatalf("tokens must come from the rollup: %+v", row)
	}
	if !row.hasCost || row.costBRL <= 0 {
		t.Fatalf("cost must be estimated from the child model pricing: %+v", row)
	}
	if want := base.Add(-90 * time.Second); !row.startedAt.Equal(want) {
		t.Fatalf("startedAt must come from the start payload, got %v", row.startedAt)
	}
}

func TestSubagentsAdoptStartedAtFromStore(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.activeSession = sessions.Metadata{SessionID: "parent-1"}
	m.sessionStore = subagentsStoreWithRollup(t, "parent-1", "child-1", 100, 0, "", base.Add(-90*time.Second).Format(time.RFC3339Nano))
	m.subagents.upsertStart("call-1", "reviewer", "", false, false, 1, base)
	row := m.subagents.rows[0]
	if sessionID, startedAt := m.resolveSubagentSession(row); sessionID != "child-1" || !startedAt.Equal(base.Add(-90*time.Second)) {
		t.Fatalf("store start event must resolve session and start, got %q %v", sessionID, startedAt)
	}
	m.refreshSubagentUsage(&m.subagents.rows[0])
	if got := m.subagents.rows[0].startedAt; !got.Equal(base.Add(-90 * time.Second)) {
		t.Fatalf("startedAt must come from the start payload, got %v", got)
	}
}

func TestSubagentsSwarmRowsFromTranscript(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.activeRunID = 7
	call := transcriptRow{kind: rowToolCall, tool: "swarm_spawn", detail: "mapear auth", runID: 7}
	updated, _ := m.Update(agentRowMsg{runID: 7, row: call})
	m = updated.(model)
	res := transcriptRow{kind: rowToolResult, tool: "swarm_spawn", detail: "Spawned worker as task subagent-1 on team t.", runID: 7}
	updated, _ = m.Update(agentRowMsg{runID: 7, row: res})
	m = updated.(model)
	rows := m.subagents.visibleRows(base)
	if len(rows) != 1 || rows[0].key != "subagent-1" || !rows[0].swarm || rows[0].state != subagentRunning {
		t.Fatalf("swarm spawn must create a running row: %#v", rows)
	}
	status := transcriptRow{kind: rowToolResult, tool: "swarm_status", detail: "- subagent-1 [done] worker", runID: 7}
	updated, _ = m.Update(agentRowMsg{runID: 7, row: status})
	m = updated.(model)
	rows = m.subagents.visibleRows(base)
	if len(rows) != 1 || rows[0].state != subagentDone {
		t.Fatalf("swarm status done must finish the row: %#v", rows)
	}
}

func TestSubagentsEnterRunningResolvesChildSession(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	// Running row: keyed by tool call id, child session only in the log.
	if _, err := m.sessionStore.Create(sessions.CreateInput{SessionID: "parent-1"}); err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	if _, err := m.sessionStore.Create(sessions.CreateInput{SessionID: "sess-9"}); err != nil {
		t.Fatalf("create child session: %v", err)
	}
	start := map[string]any{
		"source": "specialist", "childSessionId": "sess-9", "toolCallId": "call-9",
		"startedAt": base.Add(-25 * time.Second).Format(time.RFC3339Nano),
	}
	if _, err := m.sessionStore.AppendEvent("parent-1", sessions.AppendEventInput{Type: sessions.EventSpecialistStart, Payload: start}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	m.activeSession = sessions.Metadata{SessionID: "parent-1"}
	m.subagents.upsertStart("call-9", "explorer", "mapear", false, false, 1, base)
	m.subagents.expanded = true
	m.subagents.selected = 0

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !m.subchat.active || m.subchat.childSessionID != "sess-9" {
		t.Fatalf("Enter on a running subagent must open the real child session, active=%v child=%q", m.subchat.active, m.subchat.childSessionID)
	}
}

type subagentBgReconcileFake struct {
	subagentStopFake
	calls int
	task  background.Task
	ok    bool
}

func (fake *subagentBgReconcileFake) BackgroundTask(taskID string) (background.Task, bool) {
	fake.calls++
	return fake.task, fake.ok
}

// a background row must settle once the manager reports it finished, and
// the 2s throttle must persist between polls.
func TestSubagentsBackgroundRowReconcilesWithManager(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.width = 100
	fake := &subagentBgReconcileFake{task: background.Task{Status: background.StatusRunning}, ok: true}
	reg := tools.NewRegistry()
	reg.Register(fake)
	m.registry = reg
	m.subagents.upsertStart("task-1", "bg", "", false, true, 1, base)

	m.housekeepSubagents(base)
	if row, _ := m.subagents.rowByKey("task-1"); row.state != subagentRunning {
		t.Fatalf("a running manager task must stay running: %+v", row)
	}
	m.housekeepSubagents(base)
	if fake.calls != 1 {
		t.Fatalf("the 2s throttle must persist across polls, got %d manager calls", fake.calls)
	}
	fake.task = background.Task{Status: background.StatusCompleted, CompletedAt: base.Add(time.Second)}
	m.housekeepSubagents(base.Add(3 * time.Second))
	row, _ := m.subagents.rowByKey("task-1")
	if row.state != subagentDone {
		t.Fatalf("a completed manager task must settle the row: %+v", row)
	}
	if !row.finishedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("the row must settle at the manager completion time: %+v", row)
	}
}

// ending the run fails leftover foreground rows but keeps background ones.
func TestSubagentsSettleRunFailsForegroundOnly(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("fg", "explorer", "", false, false, 1, base)
	m.subagents.upsertStart("bg", "indexer", "", false, true, 1, base)

	m.subagents.settleRun(base)
	fg, _ := m.subagents.rowByKey("fg")
	if fg.state != subagentFailed || fg.detail != "turno encerrado" || fg.finishedAt.IsZero() {
		t.Fatalf("settleRun must fail the foreground row: %+v", fg)
	}
	bg, _ := m.subagents.rowByKey("bg")
	if bg.state != subagentRunning {
		t.Fatalf("settleRun must keep the background row: %+v", bg)
	}
}

// cancelling stops leftover foreground rows but keeps background ones.
func TestSubagentsCancelAllStopsForegroundOnly(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.subagents.upsertStart("fg", "explorer", "", false, false, 1, base)
	m.subagents.upsertStart("bg", "indexer", "", false, true, 1, base)

	m.subagents.cancelAll(base)
	fg, _ := m.subagents.rowByKey("fg")
	if fg.state != subagentStopped || fg.detail != "execução cancelada" || fg.finishedAt.IsZero() {
		t.Fatalf("cancelAll must stop the foreground row: %+v", fg)
	}
	bg, _ := m.subagents.rowByKey("bg")
	if bg.state != subagentRunning {
		t.Fatalf("cancelAll must keep the background row: %+v", bg)
	}
}

// completion must read the usage rollup from the store through Update.
func TestSubagentsCompletionReadsStoreRollup(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.activeRunID = 9
	m.activeSession = sessions.Metadata{SessionID: "parent-1"}
	m.sessionStore = subagentsStoreWithRollup(t, "parent-1", "child-1", 8000, 100, "modelo-teste", base.Add(-90*time.Second).Format(time.RFC3339Nano))
	m.modelName = "modelo-teste"
	m.providerName = "qg"
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"qg": {{
			ID: "modelo-teste", Source: "live",
			APIModel: &qgaccount.ModelInfo{
				ID:         "modelo-teste",
				PricingBRL: qgaccount.ModelPricing{Currency: "BRL", Input: "0.5", Output: "1"},
			},
		}},
	}
	m.subagents.upsertStart("call-1", "reviewer", "", false, false, 9, base)

	updated, _ := m.Update(specialistCompleteMsg{runID: 9, toolCallID: "call-1", childSessionID: "child-1", status: specialistCompleted})
	m = updated.(model)
	row, ok := m.subagents.rowByKey("child-1")
	if !ok || !row.hasUsage || row.promptTokens+row.completionTokens != 8100 {
		t.Fatalf("completion must read the store rollup: %+v", row)
	}
	if !row.hasCost || row.costBRL <= 0 {
		t.Fatalf("completion must price the rollup: %+v", row)
	}
}

func TestSubchatPollReloadThrottlesAndFollowsChanges(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := subchatPanelTestModel(t, base)
	if errText := m.subchat.enter(m.sessionStore, "sess-1", "sleeper", 0); errText != "" {
		t.Fatalf("enter: %s", errText)
	}
	m.subchat.pollReload(m.sessionStore, base)
	if len(m.subchat.childRows) != 0 {
		t.Fatalf("nothing changed: poll must not load rows: %+v", m.subchat.childRows)
	}
	msg := map[string]any{"role": "assistant", "content": "hello"}
	if _, err := m.sessionStore.AppendEvent("sess-1", sessions.AppendEventInput{Type: sessions.EventMessage, Payload: msg}); err != nil {
		t.Fatalf("append: %v", err)
	}
	m.subchat.pollReload(m.sessionStore, base)
	if len(m.subchat.childRows) != 0 {
		t.Fatalf("same second: poll must throttle the re-read")
	}
	m.subchat.pollReload(m.sessionStore, base.Add(2*time.Second))
	if len(m.subchat.childRows) == 0 {
		t.Fatal("after 1s with a changed log the poll must reload")
	}
}

func TestTaskUsageTotalsSumsDeltasAndKeepsRunStartModel(t *testing.T) {
	totals := &taskUsageTotals{}
	totals.add(streamjson.Event{Type: streamjson.EventRunStart, Model: "modelo-teste"})
	prompt, completion := 1000, 200
	totals.add(streamjson.Event{Type: streamjson.EventUsage, PromptTokens: &prompt, CompletionTokens: &completion})
	prompt, completion = 2000, 300
	totals.add(streamjson.Event{Type: streamjson.EventUsage, PromptTokens: &prompt, CompletionTokens: &completion})
	if totals.prompt != 3000 || totals.completion != 500 {
		t.Fatalf("producer must sum per-call deltas: %+v", totals)
	}
	if totals.modelID != "modelo-teste" {
		t.Fatalf("producer must keep the run_start model: %+v", totals)
	}

	fallback := &taskUsageTotals{}
	fallback.add(streamjson.Event{Type: streamjson.EventRunStart, APIModel: "api-modelo"})
	if fallback.modelID != "api-modelo" {
		t.Fatalf("producer must fall back to the API model id: %+v", fallback)
	}
}

// cancelling the run through Update must stop the foreground rows.
func TestSubagentsCancelRunStopsForegroundRows(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.pending = true
	m.subagents.upsertStart("fg", "explorer", "", false, false, 1, base)
	m.subagents.upsertStart("bg", "indexer", "", false, true, 1, base)

	updated, _ := m.Update(testKeyCtrl('c'))
	m = updated.(model)
	fg, _ := m.subagents.rowByKey("fg")
	if fg.state != subagentStopped || fg.detail != "execução cancelada" {
		t.Fatalf("cancelling the run must stop the foreground row: %+v", fg)
	}
	if bg, _ := m.subagents.rowByKey("bg"); bg.state != subagentRunning {
		t.Fatalf("cancelling the run must keep the background row: %+v", bg)
	}
}

// a finished run through Update must fail the leftover foreground rows.
func TestSubagentsRunFinishSettlesForegroundRows(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := panelTestModel(base)
	m.activeRunID = 7
	m.subagents.upsertStart("fg", "explorer", "", false, false, 7, base)

	updated, _ := m.Update(agentResponseMsg{runID: 7})
	m = updated.(model)
	if fg, _ := m.subagents.rowByKey("fg"); fg.state != subagentFailed {
		t.Fatalf("finishing the run must settle the foreground row: %+v", fg)
	}
}
