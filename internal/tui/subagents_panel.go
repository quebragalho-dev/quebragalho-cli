package tui

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/background"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/specialist"
)

// composerCursorOnLastVisualLine reports whether the composer cursor sits on
// the last visual line (or the composer is empty/single-line): ↓ from here
// has nowhere to go in the composer, so the subagents panel may take it.
func (m model) composerCursorOnLastVisualLine() bool {
	width := chatWidth(m.width)
	if width < 8 {
		return true
	}
	state := normalizeComposerState(m.currentComposerState())
	if state.text == "" {
		return true
	}
	previews := validComposerPastePreviews(state, m.composerPastePreviews)
	displayState := composerDisplayStateForPastePreviews(state, previews)
	segments := composerWrappedVisualLines(m.input, displayState, maxInt(1, width-4))
	if len(segments) <= 1 {
		return true
	}
	return composerCursorVisualLine(segments, displayState.cursor) >= len(segments)-1
}

// subagentsEnterOpens reports whether Enter opens the selected subagent
// instead of submitting: expanded panel with visible rows, nothing modal,
// and not inside a drill-in already.
func (m model) subagentsEnterOpens() bool {
	return m.subagents.expanded && m.subagentsPanelVisible() && m.noBlockingModal() && !m.subchat.active
}

// openSelectedSubagent enters the selected row's child transcript. The
// drill-in order holds stable row keys (tool call ids while running); the
// real child session is resolved transiently for the enter and never written
// back, so entry, sibling stepping and the open-row lookup always agree.
func (m model) openSelectedSubagent() (model, tea.Cmd) {
	rows := m.subagents.visibleRows(m.now())
	if m.subagents.selected < 0 || m.subagents.selected >= len(rows) {
		return m, nil
	}
	row := rows[m.subagents.selected]
	sessionID := row.sessionID
	if sessionID == "" {
		// A running row is keyed by tool call id until the child session
		// exists; resolve the real child session from the parent log so Enter
		// opens the live transcript instead of an empty conversation.
		if resolved, _ := m.resolveSubagentSession(row); resolved != "" {
			sessionID = resolved
		}
	}
	if sessionID == "" {
		sessionID = row.key
	}
	if strings.TrimSpace(sessionID) == "" {
		m = m.showTransientNoticeInline("sessão ainda desconhecida", transientNoticeWarning)
		return m, nil
	}
	title := row.name
	if strings.TrimSpace(row.description) != "" {
		title += " · " + strings.TrimSpace(row.description)
	}
	if errText := m.subchat.enter(m.sessionStore, sessionID, title, m.chatScrollOffset); errText != "" {
		m = m.showTransientNoticeInline(errText, transientNoticeWarning)
		return m, nil
	}
	order := make([]string, 0, len(rows))
	for i := range rows {
		order = append(order, rows[i].key)
	}
	m.subchat.panelOrder = order
	m.subchat.panelIndex = m.subagents.selected
	m.subagents.expanded = false
	return m, nil
}

// panelRowByOrderID returns the visible row behind a drill-in order entry.
// Order entries are stable row keys; a row reconciled to its child session
// id on completion still matches through the immutable call key or either
// value.
func (m model) panelRowByOrderID(id string) (subagentRow, bool) {
	for _, row := range m.subagents.visibleRows(m.now()) {
		if row.key == id || row.callKey == id || (row.sessionID != "" && row.sessionID == id) {
			return row, true
		}
	}
	return subagentRow{}, false
}

// subchatPanelOrder returns the drill-in navigation order: the stable row
// keys snapshotted on panel entry, or the current visible panel rows when the
// subchat was opened directly from a card (so ←/→ works there too).
func (m model) subchatPanelOrder() []string {
	if len(m.subchat.panelOrder) > 0 {
		return m.subchat.panelOrder
	}
	rows := m.subagents.visibleRows(m.now())
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		order = append(order, row.key)
	}
	return order
}

// stepSubchatPanel moves the drill-in to the sibling subagent at
// panelIndex+delta, wrapping around the navigation order. The order holds
// stable row keys; the sibling's child session is resolved transiently.
func (m model) stepSubchatPanel(delta int) (model, tea.Cmd) {
	order := m.subchatPanelOrder()
	if len(order) == 0 {
		return m, nil
	}
	current := m.subchat.panelIndex
	if len(m.subchat.panelOrder) == 0 {
		// Opened directly from a card: anchor on the open child session.
		current = 0
		for i, id := range order {
			if row, ok := m.panelRowByOrderID(id); ok {
				session := row.sessionID
				if session == "" {
					session = row.key
				}
				if session == m.subchat.childSessionID {
					current = i
					break
				}
			}
		}
	}
	next := (current + delta%len(order) + len(order)) % len(order)
	m.subchat.panelOrder = order
	m.subchat.panelIndex = next
	key := order[next]
	row, ok := m.panelRowByOrderID(key)
	if !ok {
		return m, nil
	}
	sessionID := row.sessionID
	if sessionID == "" {
		if resolved, _ := m.resolveSubagentSession(row); resolved != "" {
			sessionID = resolved
		}
	}
	if sessionID == "" {
		sessionID = row.key
	}
	title := row.name
	if strings.TrimSpace(row.description) != "" {
		title += " · " + strings.TrimSpace(row.description)
	}
	if errText := m.subchat.enter(m.sessionStore, sessionID, title, m.subchat.parentScrollOffset); errText != "" {
		m = m.showTransientNoticeInline(errText, transientNoticeWarning)
	}
	return m, nil
}

// subchatReadOnly reports whether the active drill-in is read-only: a panel
// subagent conversation shows the child transcript but never accepts input
// for it while the subagent runs.
func (m model) subchatReadOnly() bool {
	return m.subchat.active && len(m.subchatPanelOrder()) > 0
}

// subchatFollowsToolCall reports whether the open drill-in shows the child
// behind toolCallID. Running rows are keyed by it, so progress and usage
// for that call belong to the open conversation.
func (m model) subchatFollowsToolCall(toolCallID string) bool {
	if !m.subchat.active || toolCallID == "" {
		return false
	}
	if row, ok := m.subchatPanelRow(); ok && row.key == toolCallID {
		return true
	}
	return false
}

// reloadOpenSubchat re-reads the open child conversation when the tool call
// behind it reports progress, so the drill-in follows the child live.
func (m *model) reloadOpenSubchat(toolCallID string) {
	if m.subchatFollowsToolCall(toolCallID) {
		m.subchat.reload(m.sessionStore)
	}
}

// subagentsConfirmKey routes keys while a stop confirmation is pending. s
// stops, n/Esc backs out, everything else is swallowed.
func (m model) subagentsConfirmKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if !m.subagents.confirmStop {
		return m, nil, false
	}
	switch {
	case keyIs(msg, tea.KeyEsc):
		m.subagents.confirmStop = false
		return m, nil, true
	case keyText(msg) == "s" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl):
		key := m.subagents.confirmKey
		if m.subagents.pendingStop == nil {
			m.subagents.pendingStop = map[string]bool{}
		}
		m.subagents.pendingStop[key] = true
		m.subagents.confirmStop = false
		return m, m.buildStopCmd(key), true
	case keyText(msg) == "n" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl):
		m.subagents.confirmStop = false
		return m, nil, true
	default:
		return m, nil, true
	}
}

// subagentStopDoneMsg carries the outcome of an async panel-initiated stop.
type subagentStopDoneMsg struct {
	key string
	err error
}

// liveChildPIDFunc reads the running foreground child's pid from the
// same-process registry. A variable (not a direct call) so tests can point
// it at a real process they own without exporting test seams.
var liveChildPIDFunc = specialist.LiveChildPID

// buildStopCmd stops a row's subagent without blocking the Update loop.
// Everything shared is resolved here, in Update: the returned closure only
// captures plain values (key, session id, background flag, pid, task lister),
// so running it on a goroutine can never race the next Update.
func (m model) buildStopCmd(key string) tea.Cmd {
	row, ok := m.subagents.rowByKey(key)
	if !ok || row.state != subagentRunning {
		return func() tea.Msg {
			return subagentStopDoneMsg{key: key, err: fmt.Errorf("subagente já terminou")}
		}
	}
	sessionID := row.sessionID
	if sessionID == "" {
		sessionID, _ = m.resolveSubagentSession(row)
	}
	if sessionID == "" {
		sessionID = row.key
	}
	if row.background {
		lister, ok := m.subagentTaskTool()
		if !ok {
			return func() tea.Msg {
				return subagentStopDoneMsg{key: key, err: fmt.Errorf("sem acesso ao TaskStop")}
			}
		}
		return func() tea.Msg {
			return subagentStopDoneMsg{key: key, err: lister.KillTask(sessionID)}
		}
	}
	pid, ok := liveChildPIDFunc(sessionID)
	if !ok || pid <= 0 {
		return func() tea.Msg {
			return subagentStopDoneMsg{key: key, err: fmt.Errorf("sessão ainda sem processo")}
		}
	}
	return func() tea.Msg {
		return subagentStopDoneMsg{key: key, err: background.TerminateProcess(pid)}
	}
}

// resolveSubagentSession recovers a running row's child session id (and the
// published start wall-clock) from the parent session log: the start event
// carries the toolCallId that keyed the row. Empty/zero when the start event
// has not been written yet.
func (m model) resolveSubagentSession(row subagentRow) (string, time.Time) {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return "", time.Time{}
	}
	events, err := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if err != nil {
		return "", time.Time{}
	}
	for _, event := range events {
		if event.Type != sessions.EventSpecialistStart {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payloadString(payload, "source") != "specialist" {
			continue
		}
		child := payloadString(payload, "childSessionId")
		if child == "" {
			continue
		}
		if payloadString(payload, "toolCallId") != row.key && child != row.key && child != row.sessionID {
			continue
		}
		var startedAt time.Time
		if raw := strings.TrimSpace(payloadString(payload, "startedAt")); raw != "" {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
				startedAt = parsed
			}
		}
		return child, startedAt
	}
	return "", time.Time{}
}

// subagentTickCmd reschedules a spinner TickMsg one second out: the idle
// cadence that keeps visible subagents live after the turn ends (clock,
// retention, background reconciliation), without advancing any animation.
func subagentTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return spinner.TickMsg{} })
}

// subagentsCompactHit reports whether a mouse press lands on the compact
// subagents line (between the composer box and the status line). The footer
// layout is recomputed per click like the transcript hit test, so no
// geometry is cached on the value-receiver model.
func (m model) subagentsCompactHit(msg tea.MouseMsg) bool {
	if m.height <= 0 || m.setup.visible || m.mcpAddWizard != nil || m.mcpManager != nil ||
		m.picker != nil || m.renamePrompt != nil || m.terminalAttach != nil || m.suggestionsActive() {
		return false
	}
	if m.subagents.expanded || !m.subagentsPanelVisible() {
		return false
	}
	frame, _, _ := m.transcriptHitTestLayout()
	composerLines := viewLines(m.composerBox(frame.width))
	// Footer lines: idle row, composer box, compact line, status line.
	rect := frame.footerLineRect(1 + len(composerLines))
	return rect.contains(mouseX(msg), mouseY(msg))
}

// subchatPanelRow returns the panel row backing the current drill-in, when
// the subchat was opened from the panel.
func (m model) subchatPanelRow() (subagentRow, bool) {
	if !m.subchat.active {
		return subagentRow{}, false
	}
	order := m.subchatPanelOrder()
	if len(order) == 0 {
		return subagentRow{}, false
	}
	index := m.subchat.panelIndex
	if len(m.subchat.panelOrder) == 0 {
		// Opened directly from a card: anchor on the open child session.
		index = 0
		for i, id := range order {
			if row, ok := m.panelRowByOrderID(id); ok {
				session := row.sessionID
				if session == "" {
					session = row.key
				}
				if session == m.subchat.childSessionID {
					index = i
					break
				}
			}
		}
	}
	if index < 0 || index >= len(order) {
		return subagentRow{}, false
	}
	return m.panelRowByOrderID(order[index])
}

// subchatOpenRowKey returns the stable panel key of the open drill-in row,
// for stop confirmation and follow checks. Falls back to the child session
// id when the row is gone (retention) so callers still name something.
func (m model) subchatOpenRowKey() string {
	if row, ok := m.subchatPanelRow(); ok {
		return row.key
	}
	return m.subchat.childSessionID
}

// renderSubchatPanelNavBar renders the drill-in header for a panel subagent:
// identity on the left, live state on the right.
func (m model) renderSubchatPanelNavBar(width int) (string, bool) {
	if !m.subchat.active || len(m.subchatPanelOrder()) == 0 {
		return "", false
	}
	row, ok := m.subchatPanelRow()
	if !ok {
		return "", false
	}
	icon := subagentStateIcon(row.state)
	left := "qg › " + icon + " " + row.name
	if strings.TrimSpace(row.description) != "" {
		left += " · " + strings.TrimSpace(row.description)
	}
	right := subagentStateWord(row.state) + " · ⏱ " + formatSpecialistElapsed(subagentElapsed(row, m.now()))
	if row.hasUsage {
		right += " · " + formatPanelTokens(row.promptTokens+row.completionTokens) + " tok"
	}
	if row.hasCost {
		right += " · " + formatPanelCostBRL(row.costBRL)
	}
	return joinHeaderLine(qgTheme.ink.Render(left), qgTheme.muted.Render(right), width), true
}

// renderSubchatPanelFooter renders the drill-in footer: back/stop hints on
// the left, position and sibling navigation on the right. While a stop is
// being confirmed it shows the confirmation instead.
func (m model) renderSubchatPanelFooter(width int) (string, bool) {
	if !m.subchat.active || len(m.subchatPanelOrder()) == 0 {
		return "", false
	}
	if m.subagents.confirmStop {
		name := ""
		if row, ok := m.subchatPanelRow(); ok {
			name = row.name
		}
		return qgTheme.amber.Render(fmt.Sprintf("parar %q? (s/n)", name)), true
	}
	left := "Esc volta para a conversa principal · x para este subagente"
	order := m.subchatPanelOrder()
	index := m.subchat.panelIndex
	if len(m.subchat.panelOrder) == 0 {
		index = 0
		for i, id := range order {
			if row, ok := m.panelRowByOrderID(id); ok {
				session := row.sessionID
				if session == "" {
					session = row.key
				}
				if session == m.subchat.childSessionID {
					index = i
					break
				}
			}
		}
	}
	right := fmt.Sprintf("%d de %d · ←→ outro subagente", index+1, len(order))
	return joinHeaderLine(qgTheme.faint.Render(left), qgTheme.faint.Render(right), width), true
}

// subchatReadonlyBox renders the dashed read-only composer shown while a
// panel subagent conversation is open: the transcript is view-only.
func subchatReadonlyBox(width int) string {
	text := "Conversa do subagente: só leitura enquanto ele trabalha"
	inner := maxInt(len([]rune(text))+2, 20)
	if inner > maxInt(0, width-2) {
		inner = maxInt(0, width-2)
	}
	bar := strings.Repeat("┄", inner)
	pad := maxInt(0, (inner-len([]rune(text)))/2)
	line := "┆" + strings.Repeat(" ", pad) + text + strings.Repeat(" ", maxInt(0, inner-pad-len([]rune(text)))) + "┆"
	return qgTheme.faint.Render(bar + "\n" + line + "\n" + bar)
}

// subagentsPanelVisible reports whether the panel has rows to show right now.
func (m model) subagentsPanelVisible() bool {
	return len(m.subagents.visibleRows(m.now())) > 0
}

// subagentRunState is the lifecycle state of one visible subagent row.
type subagentRunState int

const (
	subagentRunning subagentRunState = iota
	subagentDone
	subagentFailed
	subagentStopped
)

// subagentRetention is how long a finished subagent stays visible after it
// ends, mirroring Claude Code's 30s panel retention.
const subagentRetention = 30 * time.Second

// subagentRow is one visible subagent: a Task specialist (foreground or
// background) or an individual swarm member.
type subagentRow struct {
	key              string // childSessionID when known, else the toolCallID/task id
	callKey          string // first key (tool call / task id), never rewritten
	name             string
	description      string
	swarm            bool
	background       bool
	state            subagentRunState
	detail           string // live activity, summary or error
	startedAt        time.Time
	finishedAt       time.Time
	promptTokens     int64
	completionTokens int64
	hasUsage         bool
	modelID          string
	costBRL          float64
	hasCost          bool
	sessionID        string // drill-in target, "" until known
	runID            int
}

// subagentsPanel holds the live subagent rows rendered as the compact line
// and the expanded panel. Rows are fed by the specialist/swarm message flow
// and pruned by recency, never by run boundaries, so a finished agent stays
// visible for subagentRetention after its run ends.
type subagentsPanel struct {
	rows                []subagentRow
	expanded            bool
	selected            int
	confirmStop         bool
	confirmKey          string
	pendingTask         string // unmatched swarm_spawn call detail, paired on result
	pendingRunID        int
	pendingStop         map[string]bool // keys with a user-initiated stop in flight
	nextBackgroundCheck time.Time       // throttles manager polls to every 2s
}

func (panel *subagentsPanel) find(key string) int {
	for index := range panel.rows {
		if panel.rows[index].key == key {
			return index
		}
	}
	return -1
}

// upsertStart records a newly spawned subagent. A repeated key supersedes
// the previous row (tool-call and swarm task ids can repeat across runs).
func (panel *subagentsPanel) upsertStart(key, name, description string, swarm, background bool, runID int, now time.Time) {
	entry := subagentRow{
		key:         key,
		callKey:     key,
		name:        name,
		description: description,
		swarm:       swarm,
		background:  background,
		state:       subagentRunning,
		startedAt:   now,
		runID:       runID,
	}
	if index := panel.find(key); index >= 0 {
		entry.promptTokens, entry.completionTokens = 0, 0
		panel.rows[index] = entry
	} else {
		panel.rows = append(panel.rows, entry)
	}
}

func (panel *subagentsPanel) setActivity(key, detail string) {
	if index := panel.find(key); index >= 0 {
		panel.rows[index].detail = detail
	}
}

// setUsage records rolled-up tokens (and an optional estimated BRL cost) for
// a row. It always replaces: live messages carry running totals and the
// completion rollup replaces the live values. Cost is only ever set from real
// usage events, never per frame.
func (panel *subagentsPanel) setUsage(key string, prompt, completion int64, modelID string, costBRL float64, hasCost bool) {
	if index := panel.find(key); index >= 0 {
		panel.rows[index].promptTokens = prompt
		panel.rows[index].completionTokens = completion
		panel.rows[index].hasUsage = true
		panel.rows[index].modelID = modelID
		panel.rows[index].costBRL = costBRL
		panel.rows[index].hasCost = hasCost
	}
}

// completeToolCall finishes a row keyed by tool call id, reconciling it to
// the real child session id first (the task id is only a temporary key until
// the child session exists). Background rows stay running: their Task result
// is the spawn ack, not the finish.
func (panel *subagentsPanel) completeToolCall(toolCallID, sessionID string, ok bool, errMsg string, now time.Time) {
	index := panel.find(toolCallID)
	if index < 0 {
		return
	}
	if sessionID != "" && sessionID != toolCallID {
		panel.rows[index].key = sessionID
		panel.rows[index].sessionID = sessionID
	}
	if panel.rows[index].background {
		return
	}
	if panel.pendingStop[toolCallID] || panel.pendingStop[panel.rows[index].key] {
		panel.rows[index].state = subagentStopped
		delete(panel.pendingStop, toolCallID)
		delete(panel.pendingStop, panel.rows[index].key)
	} else if ok {
		panel.rows[index].state = subagentDone
	} else {
		panel.rows[index].state = subagentFailed
		panel.rows[index].detail = errMsg
	}
	panel.rows[index].finishedAt = now
}

// visibleRows returns running rows plus rows finished within
// subagentRetention, in spawn order.
func (panel *subagentsPanel) visibleRows(now time.Time) []subagentRow {
	var out []subagentRow
	for _, row := range panel.rows {
		if row.state == subagentRunning {
			out = append(out, row)
			continue
		}
		if row.finishedAt.IsZero() || now.Sub(row.finishedAt) < subagentRetention {
			out = append(out, row)
		}
	}
	return out
}

// prune drops rows finished longer ago than the retention window and clamps
// the selection.
func (panel *subagentsPanel) prune(now time.Time) {
	kept := panel.rows[:0]
	for _, row := range panel.rows {
		if row.state == subagentRunning {
			kept = append(kept, row)
			continue
		}
		if row.finishedAt.IsZero() || now.Sub(row.finishedAt) < subagentRetention {
			kept = append(kept, row)
		}
	}
	panel.rows = kept
	if panel.selected >= len(kept) {
		panel.selected = maxInt(0, len(kept)-1)
	}
	if len(kept) == 0 {
		panel.expanded = false
		panel.confirmStop = false
	}
}

// selectNext moves the expanded-panel selection down, wrapping around.
// Changing rows drops a pending stop confirmation (it names its own row).
func (panel *subagentsPanel) selectNext() {
	if len(panel.rows) == 0 {
		return
	}
	panel.selected = (panel.selected + 1) % len(panel.rows)
	panel.confirmStop = false
}

// selectPrev moves the expanded-panel selection up, wrapping around.
func (panel *subagentsPanel) selectPrev() {
	if len(panel.rows) == 0 {
		return
	}
	panel.selected = (panel.selected - 1 + len(panel.rows)) % len(panel.rows)
	panel.confirmStop = false
}

// subagentElapsed renders a row's elapsed time: live while running, frozen at
// finish otherwise.
func subagentElapsed(row subagentRow, now time.Time) time.Duration {
	end := row.finishedAt
	if row.state == subagentRunning || end.IsZero() {
		end = now
	}
	if end.Before(row.startedAt) {
		return 0
	}
	return end.Sub(row.startedAt)
}

// formatPanelTokens renders token counts pt-BR style: 23300 -> "23,3k".
func formatPanelTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return strings.Replace(strconv.FormatFloat(float64(n)/1000, 'f', 1, 64), ".", ",", 1) + "k"
	}
	return strings.Replace(strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64), ".", ",", 1) + "M"
}

// formatPanelCostBRL renders an estimated BRL cost, always with ~: values
// below one centavo keep three decimals ("~R$ 0,004").
func formatPanelCostBRL(cost float64) string {
	if cost < 0 {
		cost = 0
	}
	if cost > 0 && cost < 0.01 {
		return "~R$ " + strings.Replace(strconv.FormatFloat(cost, 'f', 3, 64), ".", ",", 1)
	}
	return "~R$ " + strings.Replace(strconv.FormatFloat(cost, 'f', 2, 64), ".", ",", 1)
}

// panelTotals aggregates the visible rows: counts, elapsed since the oldest
// visible start, summed tokens and estimated cost.
func (panel *subagentsPanel) totals(now time.Time) (total, running int, elapsed time.Duration, tokens int64, hasTokens bool, cost float64, hasCost bool) {
	rows := panel.visibleRows(now)
	total = len(rows)
	if total == 0 {
		return 0, 0, 0, 0, false, 0, false
	}
	oldest := rows[0].startedAt
	for _, row := range rows {
		if row.state == subagentRunning {
			running++
		}
		if row.startedAt.Before(oldest) {
			oldest = row.startedAt
		}
		if row.hasUsage {
			hasTokens = true
			tokens += row.promptTokens + row.completionTokens
		}
		if row.hasCost {
			hasCost = true
			cost += row.costBRL
		}
	}
	elapsed = now.Sub(oldest)
	if elapsed < 0 {
		elapsed = 0
	}
	return total, running, elapsed, tokens, hasTokens, cost, hasCost
}

func subagentCountNoun(total int) string {
	if total == 1 {
		return "1 subagente"
	}
	return fmt.Sprintf("%d subagentes", total)
}

// compactSummary builds the compact line text (without styling or hint).
// The shape is fixed: `● N subagentes · M rodando · ⏱ …` plus tokens and the
// estimated cost whenever present — retention keeps finished rows visible,
// and the counter keeps counting them.
func (panel *subagentsPanel) compactSummary(now time.Time) string {
	total, running, elapsed, tokens, hasTokens, cost, hasCost := panel.totals(now)
	if total == 0 {
		return ""
	}
	parts := []string{"● " + subagentCountNoun(total), fmt.Sprintf("%d rodando", running)}
	parts = append(parts, "⏱ "+formatSpecialistElapsed(elapsed))
	if hasTokens {
		parts = append(parts, formatPanelTokens(tokens)+" tok")
	}
	if hasCost {
		parts = append(parts, formatPanelCostBRL(cost))
	}
	return strings.Join(parts, " · ")
}

func subagentStateIcon(state subagentRunState) string {
	switch state {
	case subagentRunning:
		return "●"
	case subagentDone:
		return "✓"
	case subagentFailed:
		return "✗"
	default:
		return "■"
	}
}

func subagentStateWord(state subagentRunState) string {
	switch state {
	case subagentRunning:
		return "rodando"
	case subagentDone:
		return "pronto"
	case subagentFailed:
		return "falhou"
	default:
		return "parado"
	}
}

func stateCountsLabel(counts map[subagentRunState]int) string {
	order := []subagentRunState{subagentRunning, subagentDone, subagentFailed, subagentStopped}
	words := map[subagentRunState]string{
		subagentRunning: "RODANDO",
		subagentDone:    "PRONTO",
		subagentFailed:  "COM ERRO",
		subagentStopped: "PARADO",
	}
	parts := []string{}
	for _, state := range order {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], words[state]))
		}
	}
	return strings.Join(parts, " · ")
}

// renderSubagentsExpandedBox renders the expanded panel as a footer box when
// open with visible rows. Empty otherwise (the compact line renders instead).
func (m model) renderSubagentsExpandedBox(width int) string {
	if !m.subagents.expanded {
		return ""
	}
	now := m.now()
	if len(m.subagents.visibleRows(now)) == 0 {
		return ""
	}
	body := m.renderSubagentsExpanded(width-4, now)
	if body == "" {
		return ""
	}
	return styledBlockFill(width, strings.Split(body, "\n"), qgTheme.line, qgTheme.panel)
}

// renderSubagentsExpanded renders the expanded panel content: header with
// state counts and totals, one row per visible subagent, and the footer with
// the available keys (or the stop confirmation).
func (m model) renderSubagentsExpanded(width int, now time.Time) string {
	rows := m.subagents.visibleRows(now)
	if len(rows) == 0 {
		return ""
	}
	if m.subagents.selected >= len(rows) {
		m.subagents.selected = len(rows) - 1
	}
	counts := map[subagentRunState]int{}
	for _, row := range rows {
		counts[row.state]++
	}
	_, _, elapsed, tokens, hasTokens, cost, hasCost := m.subagents.totals(now)
	total := "TOTAL ⏱ " + formatSpecialistElapsed(elapsed)
	if hasTokens {
		total += " · " + formatPanelTokens(tokens) + " tok"
	}
	if hasCost {
		total += " · " + formatPanelCostBRL(cost)
	}
	header := "SUBAGENTES · " + stateCountsLabel(counts)
	lines := []string{qgTheme.faint.Render(header + "  " + total)}
	for index, row := range rows {
		icon := subagentStateIcon(row.state)
		var styled string
		switch row.state {
		case subagentRunning, subagentDone:
			styled = qgTheme.green.Render(icon)
		case subagentFailed:
			styled = qgTheme.red.Render(icon)
		default:
			styled = qgTheme.faint.Render(icon)
		}
		text := styled + " " + row.name
		// Live activity (or the failure reason) wins over the static
		// description: the row stays on one line next to tokens and cost.
		if strings.TrimSpace(row.detail) != "" {
			text += " " + strings.TrimSpace(row.detail)
		}
		text += " · " + subagentStateWord(row.state)
		text += " · ⏱ " + formatSpecialistElapsed(subagentElapsed(row, now))
		if row.hasUsage {
			text += " · " + formatPanelTokens(row.promptTokens+row.completionTokens) + " tok"
		}
		if row.hasCost {
			text += " · " + formatPanelCostBRL(row.costBRL)
		}
		if index == m.subagents.selected {
			text = qgTheme.accent.Render("❯ ") + text
		}
		lines = append(lines, fitStyledLine(text, maxInt(1, width)))
	}
	if m.subagents.confirmStop {
		name := ""
		for _, row := range rows {
			if row.key == m.subagents.confirmKey {
				name = row.name
				break
			}
		}
		lines = append(lines, qgTheme.amber.Render(fmt.Sprintf("parar %q? (s/n)", name)))
	} else {
		lines = append(lines, qgTheme.faint.Render("↑↓ escolhe · Enter abre a conversa · x para (pede confirmação) · Ctrl+G recolhe"))
	}
	return strings.Join(lines, "\n")
}

// subagentsCompactLine renders the one-line subagents summary shown just
// below the composer box when any subagent is visible: totals plus the hint
// to expand. Empty when nothing is visible. Recomputed every frame; the
// inputs are a few rows, so no cache is kept.
func (m model) subagentsCompactLine(width int) string {
	now := m.now()
	rows := m.subagents.visibleRows(now)
	if len(rows) == 0 {
		return ""
	}
	summary := m.subagents.compactSummary(now)
	if summary == "" {
		return ""
	}
	hint := "clique, ↓ ou Ctrl+G para ampliar"
	head := summary
	tail := ""
	if index := strings.Index(summary, " "); index >= 0 {
		head, tail = summary[:index], summary[index:]
	}
	icon := qgTheme.green.Render(head)
	if strings.HasPrefix(summary, "✗") {
		icon = qgTheme.red.Render(head)
	}
	line := icon + tail + "  " + qgTheme.faint.Render(hint)
	if width > 0 && lipgloss.Width(line) > width {
		line = fitStyledLine(line, width)
	}
	return line
}

// setSwarmSessions links swarm task ids to member session ids (from a
// swarm_collect result).
func (panel *subagentsPanel) setSwarmSessions(sessions map[string]string) {
	for taskID, sessionID := range sessions {
		if index := panel.find(taskID); index >= 0 {
			panel.rows[index].sessionID = sessionID
			if panel.rows[index].key == taskID {
				panel.rows[index].key = sessionID
			}
		}
	}
}

// observeSwarmRow feeds swarm tool rows into the panel: spawn pairs create
// running rows, status/collect reports update their state.
func (panel *subagentsPanel) observeSwarmRow(row transcriptRow, runID int, now time.Time) {
	if row.runID != runID {
		return
	}
	switch row.kind {
	case rowToolCall:
		if row.tool == "swarm_spawn" {
			panel.pendingTask = strings.TrimSpace(row.detail)
			panel.pendingRunID = row.runID
		}
	case rowToolResult:
		switch row.tool {
		case "swarm_spawn":
			match := swarmSpawnRe.FindStringSubmatch(row.detail)
			if match == nil {
				return
			}
			id := match[1]
			name := shortTaskName(panel.pendingTask)
			if name == "" {
				name = id
			}
			panel.pendingTask = ""
			panel.upsertStart(id, name, "", true, false, row.runID, now)
		case "swarm_status", "swarm_collect":
			for _, mt := range swarmStatusRe.FindAllStringSubmatch(row.detail, -1) {
				index := panel.find(mt[1])
				if index < 0 {
					continue
				}
				switch strings.ToLower(mt[2]) {
				case "done", "completed":
					panel.rows[index].state = subagentDone
					panel.rows[index].finishedAt = now
				case "failed", "error":
					panel.rows[index].state = subagentFailed
					panel.rows[index].finishedAt = now
				case "cancelled", "killed", "stopped":
					panel.rows[index].state = subagentStopped
					panel.rows[index].finishedAt = now
				default:
					panel.rows[index].state = subagentRunning
				}
			}
		}
	}
}

// indexBySession finds a row by child session id, falling back to the
// pre-reconcile tool-call/task key.
func (panel *subagentsPanel) indexBySession(sessionID, toolCallID string) int {
	if index := panel.find(sessionID); index >= 0 {
		return index
	}
	return panel.find(toolCallID)
}

// rowByKey returns a copy of the row keyed by key.
func (panel *subagentsPanel) rowByKey(key string) (subagentRow, bool) {
	if index := panel.find(key); index >= 0 {
		return panel.rows[index], true
	}
	return subagentRow{}, false
}

// swarm work cannot outlive the run that spawned it, so anything still marked
// settleRun closes leftover running rows when their run ends: foreground and
// swarm work cannot outlive the run that spawned it, so anything still marked
// running is a failure. Background rows belong to the manager, not the run.
func (panel *subagentsPanel) settleRun(now time.Time) {
	for index := range panel.rows {
		row := &panel.rows[index]
		if row.state != subagentRunning || row.background {
			continue
		}
		row.state = subagentFailed
		row.detail = "turno encerrado"
		row.finishedAt = now
	}
}

// housekeepSubagents prunes expired rows every frame (cheap slice scan, no
// I/O) and reconciles running background rows with the manager at most
// every 2s.
func (m *model) housekeepSubagents(now time.Time) {
	m.subagents.prune(now)
	m.refreshBackgroundSubagents(now)
}

// backgroundTaskLister is the subset of the TaskStop tool the panel needs:
// stop a running task and read its latest known state. Resolved from the run
// registry at use time so the panel never holds tool state itself.
type backgroundTaskLister interface {
	KillTask(taskID string) error
	BackgroundTask(taskID string) (background.Task, bool)
}

func (m model) subagentTaskTool() (backgroundTaskLister, bool) {
	if m.registry == nil {
		return nil, false
	}
	tool, ok := m.registry.Get("TaskStop")
	if !ok {
		return nil, false
	}
	lister, ok := tool.(backgroundTaskLister)
	return lister, ok
}

// refreshBackgroundSubagents reconciles running background rows with the
// in-process manager, at most every 2s: finished rows settle (tokens and cost
// read once from the rollup), running rows keep their live clock. Pointer
// receiver: the throttle timestamp must persist on the live panel.
func (m *model) refreshBackgroundSubagents(now time.Time) {
	running := false
	for _, row := range m.subagents.rows {
		if row.state == subagentRunning && row.background {
			running = true
			break
		}
	}
	if !running || now.Before(m.subagents.nextBackgroundCheck) {
		return
	}
	m.subagents.nextBackgroundCheck = now.Add(2 * time.Second)
	lister, ok := m.subagentTaskTool()
	if !ok {
		return
	}
	for index := range m.subagents.rows {
		row := &m.subagents.rows[index]
		if row.state != subagentRunning || !row.background {
			continue
		}
		taskID := row.sessionID
		if taskID == "" {
			taskID = row.key
		}
		task, ok := lister.BackgroundTask(taskID)
		if !ok || task.Status == background.StatusRunning {
			continue
		}
		switch task.Status {
		case background.StatusCompleted:
			row.state = subagentDone
		case background.StatusKilled:
			row.state = subagentStopped
			row.detail = "parado"
		default:
			row.state = subagentFailed
			if row.detail == "" {
				row.detail = string(task.Status)
			}
		}
		if !task.CompletedAt.IsZero() {
			row.finishedAt = task.CompletedAt
		} else {
			row.finishedAt = now
		}
		m.refreshSubagentUsage(row)
	}
}

// refreshSubagentUsage fills a row's tokens and estimated BRL cost from the
// child's usage rollup in the parent session log, priced with the child
// model's catalog rates. It runs when usage arrives (completion, background
// finish), never per frame. No-op without a store, session, rollup or price.
func (m model) refreshSubagentUsage(row *subagentRow) {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return
	}
	// Resolve the real child session (and the published start wall-clock)
	// before reading usage, so pre-reconcile rows still find their rollup.
	if sessionID, startedAt := m.resolveSubagentSession(*row); sessionID != "" {
		row.sessionID = sessionID
		if !startedAt.IsZero() {
			row.startedAt = startedAt
		}
	}
	sessionID := row.sessionID
	if sessionID == "" {
		sessionID = row.key
	}
	events, err := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if err != nil {
		return
	}
	var prompt, completion int64
	for _, event := range events {
		if event.Type != sessions.EventUsage {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payloadString(payload, "source") != "specialist" {
			continue
		}
		if payloadString(payload, "childSessionId") != sessionID {
			continue
		}
		prompt += payloadInt64(payload, "promptTokens")
		completion += payloadInt64(payload, "completionTokens")
	}
	if prompt+completion == 0 {
		return
	}
	row.promptTokens = prompt
	row.completionTokens = completion
	row.hasUsage = true
	meta, err := m.sessionStore.Get(sessionID)
	if err != nil || meta == nil {
		return
	}
	row.modelID = strings.TrimSpace(meta.ModelID)
	info := m.apiModel(row.modelID)
	if info == nil {
		return
	}
	amount, err := info.PricingBRL.Cost(qgruntime.Usage{InputTokens: int(prompt), OutputTokens: int(completion)})
	if err != nil {
		return
	}
	cost, _ := new(big.Rat).Set(amount).Float64()
	row.costBRL = cost
	row.hasCost = true
}

// killed the foreground children, and the panel must not show them running
// forever. Background rows belong to the manager and keep running.
func (panel *subagentsPanel) cancelAll(now time.Time) {
	for index := range panel.rows {
		row := &panel.rows[index]
		if row.state != subagentRunning || row.background {
			continue
		}
		row.state = subagentStopped
		row.detail = "execução cancelada"
		row.finishedAt = now
	}
}
