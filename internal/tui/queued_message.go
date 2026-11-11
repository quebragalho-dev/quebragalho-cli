package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// queuedEditHint replaces the composer placeholder while a message is queued
// and informs the user they can press up to edit the last queued message.
const queuedEditHint = "Pressione ↑ para editar a última da fila"

// sendNowSignal wakes a waiting backgroundable step (waiting for a tool's
// yield, polling a session) so a turn can absorb the operator's messages now
// instead of interrupting them. The concrete implementation is the run's
// yieldnow.Signal, owned by the agent side; the TUI holds only this
// interface, per run. nil (or a false return) means nothing woke up, and the
// caller interrupts the turn and relaunches the messages instead.
type sendNowSignal interface {
	Request() bool
}

// interleaveBuffer holds operator messages the run should absorb at the next
// step boundary — Ctrl+Enter during a running turn (Cmd+Enter is still
// accepted when a terminal sends it). Lives outside the model value so the
// agent goroutine drains the same buffer the UI fills.
type deliveredInterleave struct {
	id   string
	text string
}

type interleaveBuffer struct {
	mu    sync.Mutex
	texts []string
	// delivered records the texts the loop already drained into the run's
	// wire (and the rows they produced), so a batch parked after an interrupt
	// can be relaunched losslessly even when the dead run never hands over
	// its continuation.
	delivered []deliveredInterleave
}

func (b *interleaveBuffer) push(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	b.mu.Lock()
	b.texts = append(b.texts, trimmed)
	b.mu.Unlock()
}

// popLast removes and returns the LAST still-pending text (one the loop has
// not drained yet), or false when the buffer holds none. One atomic step
// under the mutex: the loop can never hand out half of what ↑ edits.
func (b *interleaveBuffer) popLast() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.texts) == 0 {
		return "", false
	}
	text := b.texts[len(b.texts)-1]
	b.texts = b.texts[:len(b.texts)-1]
	return text, true
}

// pendingCount reports how many texts are still waiting to be drained by the
// loop, without copying them.
func (b *interleaveBuffer) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.texts)
}

// noteDelivered records texts the loop drained into the wire — called by
// the agent goroutine after InterleaveMessages hands messages over. ids
// match the rows emitted live.
func (b *interleaveBuffer) noteDelivered(items []deliveredInterleave) {
	b.mu.Lock()
	b.delivered = append(b.delivered, items...)
	b.mu.Unlock()
}

// takeDelivered drains the recorded deliveries: each tracked item may ride
// a relaunch at most once.
func (b *interleaveBuffer) takeDelivered() []deliveredInterleave {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.delivered) == 0 {
		return nil
	}
	out := append([]deliveredInterleave(nil), b.delivered...)
	b.delivered = nil
	return out
}

// snapshot returns a copy of the pending interleave texts, in FIFO order,
// without draining them. The transcript renders them as dimmed rows while
// they wait for the loop to consume the buffer, so they do not vanish from
// the screen between the push and the drain.
func (b *interleaveBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.texts) == 0 {
		return nil
	}
	return append([]string(nil), b.texts...)
}

// drain returns and clears the pending interleaves. Emitting rows and events
// is the caller's business (the agent goroutine reports them on the run's
// behalf; the TUI uses drain only to requeue leftovers).
func (b *interleaveBuffer) drain() []qgruntime.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.texts) == 0 {
		return nil
	}
	out := make([]qgruntime.Message, len(b.texts))
	for i, text := range b.texts {
		out[i] = qgruntime.Message{Role: qgruntime.MessageRoleUser, Content: text}
	}
	b.texts = nil
	return out
}

// queueMessage appends text to the FIFO queue of pending follow-up messages.
// Every queued message renders as a dimmed Sussurro row in the transcript; ↑
// edits only the last one; Ctrl+Enter (or /agora, when the terminal cannot
// tell Ctrl+Enter from Enter) sends the queue plus the draft now; at the end
// of the turn the head rides the next turn.
func (m model) queueMessage(text string) model {
	text = strings.TrimSpace(text)
	if text == "" {
		return m
	}
	m.queuedMessages = append(m.queuedMessages, text)
	m.rememberInput(text)
	m.clearComposer()
	m.clearSuggestions()
	return m
}

// returnTextsToComposer moves text back into the composer so it can be edited
// before the next turn. Queued text lands above whatever is already being
// typed.
func (m model) returnTextsToComposer(text string) model {
	if draft := m.composerValue(); strings.TrimSpace(draft) != "" {
		text += "\n" + draft
	}
	// Multiline text must go through the composer state: m.input only holds a
	// flattened display copy (see syncInputFromComposer).
	m.setComposerState(composerState{text: text, cursor: len([]rune(text))})
	m.recomputeSuggestions()
	return m
}

// popQueuedMessageForEdit moves the LAST queued message back into the composer
// so it can be edited before the next turn. Queued text lands above whatever
// is already being typed.
func (m model) popQueuedMessageForEdit() model {
	if !m.hasQueuedMessage() {
		return m
	}
	text := m.queuedMessages[len(m.queuedMessages)-1]
	m.queuedMessages = m.queuedMessages[:len(m.queuedMessages)-1]
	return m.returnTextsToComposer(text)
}

// popInterleaveForEdit moves the LAST still-pending interleave back into
// the composer, above whatever draft is already there. The parked send-now
// batch is the newest undelivered work, so its tail pops first; then the
// run's interleave buffer; then the FIFO tail. The loop may already have
// drained everything — then popLast reports false and ↑ falls through.
func (m model) popInterleaveForEdit() (model, bool) {
	if n := len(m.pendingSendNow); n > 0 {
		text := m.pendingSendNow[n-1]
		m.pendingSendNow = m.pendingSendNow[:n-1]
		// A window that just emptied its batch is no longer a turn.
		if len(m.pendingSendNow) == 0 && m.activeRunID == 0 {
			m.pending = false
		}
		return m.returnTextsToComposer(text), true
	}
	if m.interleave == nil {
		return m, false
	}
	text, ok := m.interleave.popLast()
	if !ok {
		return m, false
	}
	return m.returnTextsToComposer(text), true
}

// returnQueuedMessagesToComposer moves the undelivered work back into the
// composer, in order: the parked send-now batch first, then the FIFO
// (with any stranded interleaves merged at its head by the caller), all
// landing above whatever draft is already there. Used when Esc cancels the
// turn, so nothing fires on its own after the cancel — the user reviews,
// edits or clears the returned text themselves.
func (m model) returnQueuedMessagesToComposer() model {
	combinedAll := append(append([]string(nil), m.pendingSendNow...), m.queuedMessages...)
	if len(combinedAll) == 0 {
		return m
	}
	m.pendingSendNow = nil
	m.queuedMessages = nil
	combined := strings.Join(combinedAll, "\n")
	if draft := m.composerValue(); strings.TrimSpace(draft) != "" {
		combined += "\n" + draft
	}
	m.setComposerState(composerState{text: combined, cursor: len([]rune(combined))})
	m.recomputeSuggestions()
	return m
}

func (m model) clearQueuedMessages() model {
	m.queuedMessages = nil
	return m
}

func (m model) hasQueuedMessage() bool {
	return len(m.queuedMessages) > 0
}

// requeueStrandedInterleaves moves any operator interleaves the running turn
// never consumed back to the head of the queue, so a cancel (or a final
// text-only answer) does not strand them. Turn ends launch them together in
// one new turn instead (see launchQueuedMessageIfReady); this is the Esc /
// cancel path, where nothing may fire on its own afterwards.
func (m model) requeueStrandedInterleaves() model {
	if m.interleave == nil {
		return m
	}
	stranded := []string{}
	for _, message := range m.interleave.drain() {
		if text := strings.TrimSpace(message.Content); text != "" {
			stranded = append(stranded, text)
		}
	}
	if len(stranded) == 0 {
		return m
	}
	m.queuedMessages = append(append([]string{}, stranded...), m.queuedMessages...)
	return m
}

// sendNowShortcut names the chord a user can actually press to send now,
// mirroring queuedSendHint's rule: Ctrl+Enter is only offered once the
// terminal confirmed kitty key disambiguation; until then the leader chord
// Ctrl+X Ctrl+S, which works everywhere. The notice texts share this so a
// user without a disambiguated terminal is never told to press a chord
// their terminal cannot send.
func (m model) sendNowShortcut() string {
	if m.keyboardDisambiguation {
		return "Ctrl+Enter"
	}
	return "Ctrl+X Ctrl+S"
}

// queuedSendHint names the send-now shortcut in the Sussurro notice, which
// must never outpromise the terminal: Ctrl+Enter is only offered once the
// terminal confirmed kitty key disambiguation; until then the hint points at
// the leader chord Ctrl+X Ctrl+S, which works everywhere. The hint never
// speaks of ⌘ (on macOS the Command key does not reach the kitty negotiation
// at all).
func (m model) queuedSendHint() string {
	return m.sendNowShortcut() + " envia agora"
}

func flattenQueueText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// renderSussurroRows renders one dimmed `❯ <text>` block: a plain glyph on the
// first line and an indented continuation, with the text flattened onto one
// wrapped paragraph.
func renderSussurroRows(text string, wrapWidth int) []string {
	glyph := qgTheme.ink.Render("❯")
	wrapped := wrapPlainText(text, maxInt(1, wrapWidth))
	lines := make([]string, 0, len(wrapped))
	for index, line := range wrapped {
		if index == 0 {
			lines = append(lines, " "+glyph+" "+qgTheme.muted.Render(line))
			continue
		}
		lines = append(lines, "   "+qgTheme.muted.Render(line))
	}
	return lines
}

// renderQueuedTranscriptBlock renders the Sussurro rows in the transcript:
// every interleave already pushed but not yet consumed by the agent, then
// the parked send-now batch, then every queued follow-up, each as a dimmed
// `❯ <text>` row in order. One notice below the last row carries the FULL
// count (interleave + parked batch + FIFO) and points at the last message
// for ↑, which pops the same row.
func (m model) renderQueuedTranscriptBlock(width int) string {
	if width <= 0 {
		return ""
	}
	wrapWidth := maxInt(1, width-4)
	queued := []string{}
	if m.interleave != nil {
		for _, text := range m.interleave.snapshot() {
			if flattened := flattenQueueText(text); flattened != "" {
				queued = append(queued, flattened)
			}
		}
	}
	for _, text := range m.pendingSendNow {
		if flattened := flattenQueueText(text); flattened != "" {
			queued = append(queued, flattened)
		}
	}
	for _, text := range m.queuedMessages {
		if flattened := flattenQueueText(text); flattened != "" {
			queued = append(queued, flattened)
		}
	}
	if len(queued) == 0 {
		return ""
	}
	lines := make([]string, 0, len(queued)*2+1)
	for _, text := range queued {
		lines = append(lines, renderSussurroRows(text, wrapWidth)...)
	}
	lines = append(lines, "   "+qgTheme.faint.Render(fmt.Sprintf("na fila (%d) — %s · ↑ edita a última", len(queued), m.queuedSendHint())))
	blocks := []string{strings.Join(lines, "\n")}
	return strings.Join(blocks, "\n")
}
