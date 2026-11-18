package tui

type selectableListItem struct {
	Label       string
	Description string
}

type selectableListOptions struct {
	Items      []selectableListItem
	Selected   int
	Width      int
	MaxVisible int
}

const selectableListAnchorRow = 3

// listScroll stores a mouse-pinned scroll start for a selectable list. When
// unpinned (the zero value, and after any keyboard/wheel/filter action), list
// rendering falls back to selectableListStart's 3-row anchor.
type listScroll struct {
	start  int
	pinned bool
}

func (s *listScroll) pin(start int) {
	s.start = maxInt(0, start)
	s.pinned = true
}

func (s *listScroll) unpin() {
	s.start = 0
	s.pinned = false
}

func (s listScroll) resolve(total, maxVisible, selected int) int {
	if total <= maxVisible {
		return 0
	}
	if !s.pinned {
		return selectableListStart(total, maxVisible, selected)
	}
	selected = clampInt(selected, 0, total-1)
	start := clampInt(s.start, 0, total-maxVisible)
	if selected < start {
		start = selected
	} else if maxVisible > 0 && selected >= start+maxVisible {
		start = selected - maxVisible + 1
	}
	return clampInt(start, 0, total-maxVisible)
}

func (s listScroll) resolvePanel(total, count, selected int) int {
	if !s.pinned {
		return panelListStart(total, count, selected)
	}
	return s.resolve(total, count, selected)
}

func selectableListStart(total, maxVisible, selected int) int {
	if total <= maxVisible {
		return 0
	}
	start := selected - selectableListAnchorRow
	return clampInt(start, 0, total-maxVisible)
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
