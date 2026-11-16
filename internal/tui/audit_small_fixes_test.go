package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
