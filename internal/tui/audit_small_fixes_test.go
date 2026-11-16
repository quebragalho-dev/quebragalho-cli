package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// TestModelPickerPopupClickAccountsForGroupHeaders guards the non-panel model
// picker click mapping: the overlay draws one header line per group, so the
// click row must be walked against the renderer instead of mapped directly.
func TestModelPickerPopupClickAccountsForGroupHeaders(t *testing.T) {
	m := mouseTestModel()
	m.picker = &commandPicker{
		kind: pickerModel,
		items: []pickerItem{
			{Group: "Favoritos", Label: "alpha-one", Value: "alpha-one"},
			{Group: "Favoritos", Label: "alpha-two", Value: "alpha-two"},
			{Group: "Other", Label: "beta-one", Value: "beta-one"},
		},
	}

	width := chatWidth(m.width)
	overlay := m.modelPickerOverlay(width)
	lines := viewLines(overlay)
	itemLine := -1
	headerLine := -1
	for index, line := range lines {
		if strings.Contains(line, "alpha-two") {
			itemLine = index
		}
		if strings.Contains(line, "Favoritos") {
			headerLine = index
		}
	}
	if itemLine < 0 {
		t.Fatalf("overlay does not render the second item:\n%s", overlay)
	}
	if headerLine < 0 {
		t.Fatalf("overlay does not render the group header:\n%s", overlay)
	}
	rect := m.overlayMouseRect(len(lines), width)

	target, ok := m.selectModelPickerAtMouse(testMouseClick(tea.MouseLeft, width/2, rect.y+itemLine))
	if !ok {
		t.Fatalf("click on the second item of the first group selected nothing (line %d)", itemLine)
	}
	if target.Value != "alpha-two" {
		t.Fatalf("click on alpha-two selected %q", target.Value)
	}

	if _, ok := m.selectModelPickerAtMouse(testMouseClick(tea.MouseLeft, width/2, rect.y+headerLine)); ok {
		t.Fatal("click on a group header line must not select anything")
	}
}

// TestModelPickerPopupClickFollowsTheRenderedRows pins the two other things
// the click walk mirrors from the renderer: the load-error line above the
// list, and the scrolled window, where the clicked row is not the item index.
func TestModelPickerPopupClickFollowsTheRenderedRows(t *testing.T) {
	grouped := func(count int) []pickerItem {
		items := make([]pickerItem, 0, count)
		for index := 0; index < count; index++ {
			group := "Favoritos"
			if index >= count/2 {
				group = "Other"
			}
			label := "model-" + string(rune('a'+index))
			items = append(items, pickerItem{Group: group, Label: label, Value: label})
		}
		return items
	}
	cases := []struct {
		name      string
		items     []pickerItem
		selected  int
		loadError string
		click     int
	}{
		{name: "load error line above the list", items: grouped(4), loadError: "falha ao carregar", click: 3},
		{name: "scrolled window", items: grouped(16), selected: 14, click: 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mouseTestModel()
			m.picker = &commandPicker{kind: pickerModel, items: tc.items, selected: tc.selected}
			m.modelPickerLoadError = tc.loadError
			wanted := tc.items[tc.click].Value

			width := chatWidth(m.width)
			overlay := m.modelPickerOverlay(width)
			lines := viewLines(overlay)
			line := -1
			for index, text := range lines {
				if strings.Contains(text, wanted) {
					line = index
				}
			}
			if line < 0 {
				t.Fatalf("overlay does not render %s:\n%s", wanted, overlay)
			}
			if tc.loadError != "" && !strings.Contains(overlay, tc.loadError) {
				t.Fatalf("overlay does not render the load error:\n%s", overlay)
			}
			rect := m.overlayMouseRect(len(lines), width)

			target, ok := m.selectModelPickerAtMouse(testMouseClick(tea.MouseLeft, width/2, rect.y+line))
			if !ok {
				t.Fatalf("click on %s selected nothing (line %d)", wanted, line)
			}
			if target.Value != wanted || target.Index != tc.click || m.picker.selected != tc.click {
				t.Fatalf("click on %s (item %d) gave value %q, index %d, selected %d", wanted, tc.click, target.Value, target.Index, m.picker.selected)
			}
		})
	}
}

// TestSelectableUserRowSkipsAttachmentSummaryLine guards the selectable-line
// geometry of a user row with attachments: the renderer emits a blank
// delimiter line plus one attachment summary line before the text.
// TestSubchatScrollReachesTheFirstChildRow pins the frame the scroll metrics
// use: the nav bar takes a line from the body, so the largest offset must still
// bring the first child row into view.
func TestSubchatScrollReachesTheFirstChildRow(t *testing.T) {
	m := mouseTestModel()
	m.width = 80
	m.height = 24
	m.subchat.active = true
	m.subchat.childSessionTitle = "child"
	m.subchat.childRows = make([]transcriptRow, 60)
	for index := range m.subchat.childRows {
		m.subchat.childRows[index] = transcriptRow{kind: rowAssistant, text: "child row " + string(rune('A'+index%26)) + string(rune('a'+index/26))}
	}
	_, maxOffset := m.chatScrollMetrics()
	if maxOffset <= 0 {
		t.Fatalf("a long child transcript must scroll: maxOffset=%d", maxOffset)
	}

	m.chatScrollOffset = maxOffset
	top := plainRender(t, m.View())
	if !strings.Contains(top, "child row Aa") {
		t.Fatalf("the largest scroll offset (%d) does not reach the first child row:\n%s", maxOffset, top)
	}
	m.chatScrollOffset = 0
	bottom := plainRender(t, m.View())
	if strings.Contains(bottom, "child row Aa") || !strings.Contains(bottom, "child row Hc") {
		t.Fatalf("offset 0 must show the end of the child transcript:\n%s", bottom)
	}
}

func TestSelectableUserRowSkipsAttachmentSummaryLine(t *testing.T) {
	m := mouseTestModel()

	row := transcriptRow{kind: rowUser, text: "hello world", attachments: transcriptAttachmentSummary{images: 1}}
	if got := renderUserAttachmentSummary(row.attachments); got == "" {
		t.Fatal("test setup is wrong: attachment summary renders empty")
	}
	rendered, selectable := m.renderSelectableUserRow(0, row, 80, 10)
	if len(selectable) == 0 {
		t.Fatal("expected selectable lines for a text row")
	}
	lines := strings.Split(rendered, "\n")
	index := selectable[0].bodyY - 10
	if index < 0 || index >= len(lines) {
		t.Fatalf("first selectable bodyY=%d points outside the rendered row (%d lines)", selectable[0].bodyY, len(lines))
	}
	if !strings.Contains(lines[index], "hello world") {
		t.Fatalf("first text line lands on the wrong row: lines[%d]=%q", index, lines[index])
	}
	if strings.Contains(lines[index], "[Image #1]") {
		t.Fatalf("first text line overlaps the attachment summary: %q", lines[index])
	}

	plainRendered, plain := m.renderSelectableUserRow(0, transcriptRow{kind: rowUser, text: "hello world"}, 80, 10)
	_ = plainRendered
	if len(plain) == 0 {
		t.Fatal("expected selectable lines for a row without attachments")
	}
	if plain[0].bodyY != 10+1 {
		t.Fatalf("no-attachment bodyY=%d, want %d", plain[0].bodyY, 10+1)
	}
}

// TestBTWBlocksClearLikeNew proves /clear is refused inside a BTW side
// conversation, exactly like /new: both run startNewSession.
func TestBTWBlocksClearLikeNew(t *testing.T) {
	m := newBTWTestModel(t)
	side, _ := m.handleBTWCommand("")
	updated, _ := side.dispatchCommand(parseCommand("/clear"))
	got := updated.(model)
	if !got.btw.active || got.activeSession.SessionKind != sessions.SessionKindSide {
		t.Fatalf("/clear replaced the active BTW session: active=%v metadata=%#v", got.btw.active, got.activeSession)
	}
	if !transcriptContains(got.transcript, "unavailable in a BTW conversation") {
		t.Fatalf("missing blocked-command guidance: %#v", got.transcript)
	}
}

// TestSubchatScrollUsesChildTranscript guards the scroll metrics used while a
// subchat drill-in is active: they must be measured from the child rows with
// the same header/footer the renderer uses, not from the parent transcript.
func TestSubchatScrollUsesChildTranscript(t *testing.T) {
	newSubchatModel := func() model {
		m := mouseTestModel()
		m.width = 80
		m.height = 24
		m.subchat.active = true
		m.subchat.childSessionTitle = "child"
		return m
	}

	longChild := make([]transcriptRow, 80)
	for index := range longChild {
		longChild[index] = transcriptRow{kind: rowAssistant, text: "child answer line number"}
	}
	shortParent := newSubchatModel()
	shortParent.transcript = []transcriptRow{{kind: rowUser, text: "hi"}}
	shortParent.subchat.childRows = longChild
	if _, maxOffset := shortParent.chatScrollMetrics(); maxOffset <= 0 {
		t.Fatalf("long child under a short parent must scroll: maxOffset=%d", maxOffset)
	}

	shortChild := newSubchatModel()
	longParent := make([]transcriptRow, 80)
	for index := range longParent {
		longParent[index] = transcriptRow{kind: rowAssistant, text: "parent answer line number"}
	}
	shortChild.transcript = longParent
	shortChild.subchat.childRows = []transcriptRow{{kind: rowAssistant, text: "tiny"}}
	if _, maxOffset := shortChild.chatScrollMetrics(); maxOffset != 0 {
		t.Fatalf("short child under a long parent must not scroll: maxOffset=%d", maxOffset)
	}
}

func TestSpecCommandRefusedDuringCompaction(t *testing.T) {
	store := testSessionStore(t)
	active, err := store.Create(sessions.CreateInput{Title: "Active"})
	if err != nil {
		t.Fatalf("Create active returned error: %v", err)
	}
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		submitSpecScript("call-1", "Review Flow", "# Goal\n\nAdd review flow."),
	}}
	m := newSpecModeTestModel(t.TempDir(), provider, store)
	m.activeSession = active
	m.compactInFlight = true
	m.input.SetValue("/spec add review flow")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.activeSession.SessionID != active.SessionID {
		t.Fatalf("/spec during compaction replaced the active session: got %q want %q", next.activeSession.SessionID, active.SessionID)
	}
	if next.pending || cmd != nil {
		t.Fatalf("/spec during compaction must not start a run: pending=%v cmd=%v", next.pending, cmd != nil)
	}
	if !next.compactInFlight {
		t.Fatal("a refused /spec must leave the compaction in flight")
	}
	if !transcriptContains(next.transcript, "Compactação em andamento") {
		t.Fatalf("expected the compaction warning, got %#v", next.transcript)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("the provider was called %d time(s) for a refused /spec", len(provider.requests))
	}
}
