package tui

import (
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// subchatState manages the drill-in view for a specialist's child session.
// When active, the transcript body swaps to show the child session's events
// instead of the parent's. ArrowUp/Esc pops back to the parent view.
type subchatState struct {
	// active is true when the transcript is showing a child session.
	active bool
	// childSessionID is the session being viewed.
	childSessionID string
	// childSessionTitle is the display title for the subchat nav bar.
	childSessionTitle string
	// parentScrollOffset preserves the chat scroll position so popping back
	// returns to the same view.
	parentScrollOffset int
	// childRows are the rehydrated transcript rows from the child session.
	childRows []transcriptRow
	// lastPoll throttles tick-driven freshness checks to one per second;
	// lastEventCount and lastUpdatedAt skip the full re-read while the child
	// log is unchanged. Progress and usage messages reload synchronously.
	lastPoll       time.Time
	lastEventCount int
	lastUpdatedAt  string
	// panelOrder holds the stable panel row keys at drill-in time, and
	// panelIndex the open one, so ←/→ steps through the panel's subagents
	// without leaving the subchat view. Empty when the subchat was opened
	// from a transcript card instead.
	panelOrder []string
	panelIndex int
}

// enter loads the child session's events and activates the subchat view.
// It returns an error message string if the session could not be loaded.
func (s *subchatState) enter(store *sessions.Store, childSessionID, title string, parentScrollOffset int) string {
	if store == nil || childSessionID == "" {
		return "No session store available."
	}
	events, err := store.ReadEvents(childSessionID)
	if err != nil {
		return "Could not load subagent session: " + err.Error()
	}
	s.active = true
	s.childSessionID = childSessionID
	s.childSessionTitle = title
	s.parentScrollOffset = parentScrollOffset
	s.childRows = transcriptRowsFromSessionEvents(events)
	s.recordFingerprint(store)
	return ""
}

// recordFingerprint remembers the child log's event count and timestamp so
// tick-driven polls can skip the full re-read while nothing changed.
func (s *subchatState) recordFingerprint(store *sessions.Store) {
	if store == nil || s.childSessionID == "" {
		return
	}
	if meta, err := store.Get(s.childSessionID); err == nil && meta != nil {
		s.lastEventCount = meta.EventCount
		s.lastUpdatedAt = meta.UpdatedAt
	}
}

// reload re-reads the open child session's events so a live drill-in follows
// the child while it runs. No-op without a store or an open child; a store
// error keeps the last rows instead of blanking the conversation.
func (s *subchatState) reload(store *sessions.Store) {
	if store == nil || !s.active || s.childSessionID == "" {
		return
	}
	events, err := store.ReadEvents(s.childSessionID)
	if err != nil {
		return
	}
	s.childRows = transcriptRowsFromSessionEvents(events)
	s.recordFingerprint(store)
}

// pollReload re-reads the open child conversation at most once per second,
// and only when the child log gained events since the last load. Progress
// and usage messages reload synchronously; this covers anything else.
func (s *subchatState) pollReload(store *sessions.Store, now time.Time) {
	if store == nil || !s.active || s.childSessionID == "" {
		return
	}
	if !s.lastPoll.IsZero() && !now.After(s.lastPoll.Add(time.Second)) {
		return
	}
	s.lastPoll = now
	meta, err := store.Get(s.childSessionID)
	if err != nil || meta == nil {
		return
	}
	if meta.EventCount == s.lastEventCount && meta.UpdatedAt == s.lastUpdatedAt {
		return
	}
	s.reload(store)
}

// exit deactivates the subchat view and returns the saved parent scroll offset.
func (s *subchatState) exit() int {
	offset := s.parentScrollOffset
	s.active = false
	s.childSessionID = ""
	s.childSessionTitle = ""
	s.parentScrollOffset = 0
	s.childRows = nil
	s.panelOrder = nil
	s.panelIndex = 0
	return offset
}

// renderSubchatNavBar renders the navigation bar shown at the top of the
// subchat view, telling the user how to get back to the main chat.
func renderSubchatNavBar(title string, width int) string {
	nav := "← Back to main chat (ArrowUp/Esc)"
	if title != "" {
		nav += "  ·  " + truncateRunes(title, width-40)
	}
	return qgTheme.accent.Render(nav)
}
