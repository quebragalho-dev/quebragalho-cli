package mcp

import (
	"sync"
	"time"
)

// toolsListChangedMethod is the server notification that its tool list
// changed. QG never applies it mid-request: the client only marks itself
// stale, and the runtime re-lists on the next turn boundary.
const toolsListChangedMethod = "notifications/tools/list_changed"

// catalogChangedReason is the cache-accounting reason recorded whenever a
// list_changed refresh mutates the tool catalog.
const catalogChangedReason = "ferramentas mudaram"

// staleNotifier is implemented by MCP clients that observe server
// notifications. ToolsStale fires (buffered, collapsible) after a
// tools/list_changed notification.
type staleNotifier interface {
	ToolsStale() <-chan struct{}
	markToolsStale()
}

// staleSignal is the shared list_changed latch embedded by every client.
// Notifications collapse: any number of them before the next refresh costs
// exactly one re-list.
type staleSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (signal *staleSignal) ToolsStale() <-chan struct{} {
	signal.mu.Lock()
	defer signal.mu.Unlock()
	if signal.ch == nil {
		signal.ch = make(chan struct{}, 1)
	}
	return signal.ch
}

func (signal *staleSignal) markToolsStale() {
	signal.mu.Lock()
	defer signal.mu.Unlock()
	if signal.ch == nil {
		signal.ch = make(chan struct{}, 1)
	}
	select {
	case signal.ch <- struct{}{}:
	default:
	}
}

// CatalogChange records one applied catalog refresh for cache accounting:
// the catalog only ever changes between turns, never mid-request.
type CatalogChange struct {
	Server  string
	At      time.Time
	Added   []string
	Removed []string
	Reason  string
}
