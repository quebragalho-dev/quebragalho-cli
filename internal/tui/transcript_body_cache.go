package tui

import (
	"container/list"
	"sync"
)

const defaultTranscriptBodyHeightCacheMaxEntries = 512

// transcriptHeightKey is the exact identity of one measured body item. Block
// items carry their rendered block as a string header (no copy of the bytes);
// row items embed the render row key itself. Struct equality is exact, so two
// different items never share a height.
type transcriptHeightKey struct {
	version string
	kind    transcriptBodyItemKind
	width   int
	gutter  int
	block   string
	row     renderRowKey
}

type transcriptBodyHeightCache struct {
	mu         sync.Mutex
	maxEntries int
	items      map[transcriptHeightKey]*list.Element
	lru        *list.List
}

type transcriptBodyHeightCacheEntry struct {
	key    transcriptHeightKey
	height int
}

func newTranscriptBodyHeightCache(maxEntries int) *transcriptBodyHeightCache {
	return &transcriptBodyHeightCache{
		maxEntries: maxEntries,
		items:      map[transcriptHeightKey]*list.Element{},
		lru:        list.New(),
	}
}

func (c *transcriptBodyHeightCache) get(key transcriptHeightKey) (int, bool) {
	if c == nil || key.version == "" {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return 0, false
	}
	c.lru.MoveToFront(element)
	return element.Value.(*transcriptBodyHeightCacheEntry).height, true
}

func (c *transcriptBodyHeightCache) set(key transcriptHeightKey, height int) {
	if c == nil || key.version == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntries <= 0 {
		return
	}
	if element, ok := c.items[key]; ok {
		element.Value.(*transcriptBodyHeightCacheEntry).height = height
		c.lru.MoveToFront(element)
		return
	}
	c.items[key] = c.lru.PushFront(&transcriptBodyHeightCacheEntry{key: key, height: height})
	for len(c.items) > c.maxEntries {
		element := c.lru.Back()
		if element == nil {
			return
		}
		entry := element.Value.(*transcriptBodyHeightCacheEntry)
		delete(c.items, entry.key)
		c.lru.Remove(element)
	}
}

// reserve grows the cache so that one measuring pass over items entries can
// never evict its own entries. Measurement walks the transcript in order, and an
// LRU smaller than the walk is the worst case for that pattern: each entry is
// evicted just before the walk comes back to it, so a long session missed on
// every row of every frame and re-rendered its whole history. Twice the walk
// also keeps the entries of the snapshot being replaced while a rebuilt one is
// measured, and lets the chat, detailed and subchat views share the cache
// without evicting each other. A cache built with no room stays disabled.
func (c *transcriptBodyHeightCache) reserve(items int) {
	if c == nil || items <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntries <= 0 {
		return
	}
	if want := 2 * items; want > c.maxEntries {
		c.maxEntries = want
	}
}

// transcriptSettledLayout memoizes the measured geometry of one settled
// alt-screen snapshot (altScreenSettledItems). A settled row can never change
// again, so each item is measured once and every later frame, hover and scroll
// reuses the spans instead of rendering the history again to learn its height.
// Only items whose height is declared unstable (heightCacheStable false, such as
// a running specialist card) are measured on every call, and the spans are
// rebuilt only when one of them actually changed height. A rebuilt snapshot gets
// a new layout, so a layout never has to be invalidated.
type transcriptSettledLayout struct {
	mu sync.Mutex
	// count is the number of snapshot items this layout describes; they are
	// always the first count items of a measured slice.
	count    int
	measured bool
	heights  []int
	volatile []int
	spans    []transcriptBodyItemSpan
}

func newTranscriptSettledLayout(count int) *transcriptSettledLayout {
	return &transcriptSettledLayout{count: count}
}

// settledLayoutPrefix returns the memoized layout of the settled snapshot that
// items starts with, or nil. Every snapshot item carries its layout and the
// snapshot is always placed first and whole: the live tail is appended after it
// and never carries one. Checking both ends keeps a slice that merely starts with
// snapshot items, or holds only part of the snapshot, on the generic path.
func settledLayoutPrefix(items []transcriptBodyItem) *transcriptSettledLayout {
	if len(items) == 0 {
		return nil
	}
	layout := items[0].settled
	if layout == nil || layout.count <= 0 || len(items) < layout.count || items[layout.count-1].settled != layout {
		return nil
	}
	return layout
}

// measure returns the spans of the snapshot items, measuring each stable item
// only on the first call. The returned slice is shared by every caller and must
// be treated as read-only; when a volatile item changes height a new slice is
// built, so spans handed out earlier never change under their holder.
func (l *transcriptSettledLayout) measure(items []transcriptBodyItem, cache *transcriptBodyHeightCache) []transcriptBodyItemSpan {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.measured {
		l.heights = make([]int, len(items))
		for index, item := range items {
			l.heights[index] = transcriptBodyItemHeight(item, cache)
			if !item.heightCacheStable {
				l.volatile = append(l.volatile, index)
			}
		}
		l.spans = settledLayoutSpans(items, l.heights)
		l.measured = true
		return l.spans
	}
	changed := false
	for _, index := range l.volatile {
		if height := transcriptBodyItemHeight(items[index], cache); height != l.heights[index] {
			l.heights[index] = height
			changed = true
		}
	}
	if changed {
		l.spans = settledLayoutSpans(items, l.heights)
	}
	return l.spans
}

// transcriptDetailedSnapshot holds the detailed view's (Ctrl+O) items for a
// settled snapshot. The detailed view used to rebuild every row descriptor and
// re-measure the whole transcript on each frame; built once instead, its items
// share one memoized layout just like the chat view's snapshot. It is created
// empty with each snapshot and filled the first time the detailed view is drawn,
// so sessions that never open the detailed view pay nothing.
type transcriptDetailedSnapshot struct {
	mu    sync.Mutex
	built bool
	items []transcriptBodyItem
}

func (s *transcriptDetailedSnapshot) load() ([]transcriptBodyItem, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[:len(s.items):len(s.items)], s.built
}

// store keeps the first set of items built for the snapshot, tagged with their
// memoized layout, and returns what is stored. The slice is capped at its
// length so a caller appending to it can never write into the shared array.
func (s *transcriptDetailedSnapshot) store(items []transcriptBodyItem) []transcriptBodyItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.built {
		stored := make([]transcriptBodyItem, len(items))
		copy(stored, items)
		layout := newTranscriptSettledLayout(len(stored))
		for index := range stored {
			stored[index].settled = layout
		}
		s.items = stored
		s.built = true
	}
	return s.items[:len(s.items):len(s.items)]
}

func settledLayoutSpans(items []transcriptBodyItem, heights []int) []transcriptBodyItemSpan {
	spans := make([]transcriptBodyItemSpan, len(items))
	startY := 0
	for index, item := range items {
		spans[index] = transcriptBodyItemSpan{
			kind:     item.kind,
			rowIndex: item.rowIndex,
			startY:   startY,
			height:   heights[index],
		}
		startY += heights[index]
	}
	return spans
}
