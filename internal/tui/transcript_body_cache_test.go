package tui

import (
	"fmt"
	"testing"
)

// TestTranscriptHeightCacheReserveKeepsWholeWalk pins the LRU sizing: walking
// more items than the cache was built for must not evict the walk's own
// entries, or the next walk misses on every item.
func TestTranscriptHeightCacheReserveKeepsWholeWalk(t *testing.T) {
	cache := newTranscriptBodyHeightCache(8)
	renders := make([]int, 20)
	items := make([]transcriptBodyItem, len(renders))
	for index := range items {
		items[index] = countingTranscriptBodyItem(fmt.Sprintf("row-%d", index), []string{"line"}, &renders[index])
	}
	measureTranscriptBodyItems(items, cache)
	measureTranscriptBodyItems(items, cache)
	for index, count := range renders {
		if count != 1 {
			t.Fatalf("item %d rendered %d times across two walks, want 1 (renders=%v)", index, count, renders)
		}
	}
	disabled := newTranscriptBodyHeightCache(0)
	disabled.reserve(100)
	disabled.set(transcriptBlockBodyHeightCacheKey(transcriptBodyItemRow, "row"), 1)
	if _, ok := disabled.get(transcriptBlockBodyHeightCacheKey(transcriptBodyItemRow, "row")); ok {
		t.Fatalf("reserve enabled a cache that was built disabled")
	}
}

// TestTranscriptSettledLayoutMeasuresOnce checks the memoized snapshot layout:
// stable items are measured once, a volatile item is measured on every call and
// only its height change rebuilds the spans, and spans already handed out never
// change under their holder.
func TestTranscriptSettledLayoutMeasuresOnce(t *testing.T) {
	stableRenders := [2]int{}
	volatileRenders := 0
	volatileLines := []string{"v0"}
	layout := newTranscriptSettledLayout(3)
	items := []transcriptBodyItem{
		countingTranscriptBodyItem("a", []string{"a0", "a1"}, &stableRenders[0]),
		{
			kind:     transcriptBodyItemRow,
			rowIndex: 1,
			render: func(int) transcriptBodyRenderedItem {
				volatileRenders++
				return transcriptBodyRenderedItem{lines: volatileLines}
			},
		},
		countingTranscriptBodyItem("c", []string{"c0"}, &stableRenders[1]),
		{kind: transcriptBodyItemPendingInterim, render: func(int) transcriptBodyRenderedItem {
			return transcriptBodyRenderedItem{lines: []string{"tail"}}
		}},
	}
	for index := 0; index < layout.count; index++ {
		items[index].settled = layout
	}
	cache := newTranscriptBodyHeightCache(8)

	first := measureTranscriptBodyItems(items, cache)
	if got := first.totalLines(); got != 5 {
		t.Fatalf("total lines = %d, want 2+1+1 settled + 1 tail", got)
	}
	if len(first.settled) != 3 || len(first.spans) != 1 || first.spans[0].startY != 4 {
		t.Fatalf("layout = settled %#v tail %#v, want 3 memoized spans and the tail continuing at 4", first.settled, first.spans)
	}

	second := measureTranscriptBodyItems(items, cache)
	if stableRenders != [2]int{1, 1} {
		t.Fatalf("stable items rendered %v times, want once each", stableRenders)
	}
	if volatileRenders != 2 {
		t.Fatalf("volatile item rendered %d times, want once per measure", volatileRenders)
	}
	if &second.settled[0] != &first.settled[0] {
		t.Fatalf("unchanged heights rebuilt the memoized spans")
	}

	volatileLines = []string{"v0", "v1", "v2"}
	third := measureTranscriptBodyItems(items, cache)
	if got := third.totalLines(); got != 7 {
		t.Fatalf("total lines after the volatile item grew = %d, want 7", got)
	}
	if third.settled[2].startY != 5 || third.spans[0].startY != 6 {
		t.Fatalf("spans after growth = settled %#v tail %#v, want later items shifted by two", third.settled, third.spans)
	}
	if first.settled[2].startY != 3 || first.totalLines() != 5 {
		t.Fatalf("spans handed out earlier changed: %#v", first.settled)
	}
	if stableRenders != [2]int{1, 1} {
		t.Fatalf("stable items re-rendered after a volatile height change: %v", stableRenders)
	}

	if prefix := settledLayoutPrefix(items[:2]); prefix != nil {
		t.Fatalf("a slice holding part of the snapshot was served from its layout")
	}
	if prefix := settledLayoutPrefix(items[1:]); prefix != nil {
		t.Fatalf("a slice not starting at the snapshot was served from its layout")
	}
}
