package tui

import (
	"runtime"
	"strings"
	"sync"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// queuedEditHint replaces the composer placeholder while a message is queued
// and informs the user they can press up to edit the last queued message.
const queuedEditHint = "Pressione ↑ para editar as mensagens na fila"

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

// queuedMessageHead reports the first queued message (the one the next turn
// would send), flattened onto one line for display.
func (m model) queuedMessageHead() string {
	for _, queued := range m.queuedMessages {
		if flattened := strings.Join(strings.Fields(queued), " "); flattened != "" {
			return flattened
		}
	}
	return ""
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

func queuedEditHintForPlatform() string {
	// Go's stdlib does not expose the terminal's own key naming; the
	// conventional hint is ⌘⏎ on macOS and Ctrl+Enter everywhere else.
	if runtime.GOOS == "darwin" {
		return "na fila — ⌘⏎ envia agora · ↑ edita"
	}
	return "na fila — Ctrl+Enter envia agora · ↑ edita"
}

// renderQueuedTranscriptBlock renders the Sussurro row in the transcript for
// the head of the queue: the queued message with dimmed ink and a plain `❯`
// glyph, plus a subtle hint below naming the send and edit shortcuts.
func (m model) renderQueuedTranscriptBlock(width int) string {
	head := m.queuedMessageHead()
	if head == "" || width <= 0 {
		return ""
	}
	wrapWidth := maxInt(1, width-4)
	wrapped := wrapPlainText(head, wrapWidth)
	lines := make([]string, 0, len(wrapped)+1)
	glyph := qgTheme.ink.Render("❯")
	for index, line := range wrapped {
		if index == 0 {
			lines = append(lines, " "+glyph+" "+qgTheme.muted.Render(line))
			continue
		}
		lines = append(lines, "   "+qgTheme.muted.Render(line))
	}
	lines = append(lines, "   "+qgTheme.faint.Render(queuedEditHintForPlatform()))
	return strings.Join(lines, "\n")
}
