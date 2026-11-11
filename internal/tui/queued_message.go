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

// interleaveBuffer holds operator messages the run should absorb at the next
// step boundary — Cmd+Enter / Ctrl+Enter during a running turn. Lives outside
// the model value so the agent goroutine drains the same buffer the UI fills.
type interleaveBuffer struct {
	mu    sync.Mutex
	texts []string
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
// The head renders in the transcript; ↑ edits only the tail; Cmd+Enter sends
// the head interleaved into the running turn; at the end of the turn the head
// rides the next turn.
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

// popQueuedMessageForEdit moves the LAST queued message back into the composer
// so it can be edited before the next turn. Queued text lands above whatever
// is already being typed.
func (m model) popQueuedMessageForEdit() model {
	if !m.hasQueuedMessage() {
		return m
	}
	text := m.queuedMessages[len(m.queuedMessages)-1]
	m.queuedMessages = m.queuedMessages[:len(m.queuedMessages)-1]
	if draft := m.composerValue(); strings.TrimSpace(draft) != "" {
		text += "\n" + draft
	}
	// Multiline text must go through the composer state: m.input only holds a
	// flattened display copy (see syncInputFromComposer).
	m.setComposerState(composerState{text: text, cursor: len([]rune(text))})
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
// text-only answer) does not strand them.
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

// queuedSendHint names the send-now shortcut in the Sussurro notice, which
// must never outpromise the terminal: Ctrl+Enter is only offered once the
// terminal confirmed kitty key disambiguation; until then the hint points at
// /agora, which works everywhere. The hint never speaks of ⌘ (on macOS the
// Command key does not reach the kitty negotiation at all).
func (m model) queuedSendHint() string {
	if m.keyboardDisambiguation {
		return "Ctrl+Enter envia agora"
	}
	return "/agora envia agora"
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
// every queued follow-up, each as a dimmed `❯ <text>` row in order. A notice
// below the last row of each kind names what happens next; the queue notice
// carries the full count and points at the last queued message for ↑.
func (m model) renderQueuedTranscriptBlock(width int) string {
	if width <= 0 {
		return ""
	}
	wrapWidth := maxInt(1, width-4)
	blocks := []string{}
	if m.interleave != nil {
		lines := []string{}
		for _, text := range m.interleave.snapshot() {
			if flattened := flattenQueueText(text); flattened != "" {
				lines = append(lines, renderSussurroRows(flattened, wrapWidth)...)
			}
		}
		if len(lines) > 0 {
			lines = append(lines, "   "+qgTheme.faint.Render("enviando após a ação atual…"))
			blocks = append(blocks, strings.Join(lines, "\n"))
		}
	}
	queued := []string{}
	for _, text := range m.queuedMessages {
		if flattened := flattenQueueText(text); flattened != "" {
			queued = append(queued, flattened)
		}
	}
	if len(queued) > 0 {
		lines := make([]string, 0, len(queued)*2+1)
		for _, text := range queued {
			lines = append(lines, renderSussurroRows(text, wrapWidth)...)
		}
		lines = append(lines, "   "+qgTheme.faint.Render(fmt.Sprintf("na fila (%d) — %s · ↑ edita a última", len(queued), m.queuedSendHint())))
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n")
}
