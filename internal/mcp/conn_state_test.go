package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLegalConnTransitionTable(t *testing.T) {
	states := []connState{connAlive, connDead, connClosed}
	events := []connEvent{connEventAcquire, connEventRelease, connEventMarkDead, connEventSwap, connEventClose}
	want := map[connState]map[connEvent]bool{
		connAlive: {
			connEventAcquire: true, connEventRelease: true,
			connEventMarkDead: true, connEventSwap: true, connEventClose: true,
		},
		connDead: {
			connEventAcquire: true, connEventRelease: true,
			connEventMarkDead: true, connEventSwap: true, connEventClose: true,
		},
		connClosed: {
			connEventAcquire: false, connEventRelease: true,
			connEventMarkDead: false, connEventSwap: false, connEventClose: false,
		},
	}
	for _, from := range states {
		for _, event := range events {
			_, ok := legalConnTransition(from, event)
			if ok != want[from][event] {
				t.Fatalf("legalConnTransition(%s, %s) ok = %v, want %v", from, event, ok, want[from][event])
			}
		}
	}
	if connAlive.String() != "alive" || connDead.String() != "dead" || connClosed.String() != "closed" {
		t.Fatal("connState String must stay stable")
	}
}

// genCloseCountingClient tracks Close calls for the no-fd-leak proof.
type genCloseCountingClient struct {
	closes  atomic.Int32
	block   chan struct{}
	release chan struct{}
}

func (c *genCloseCountingClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "echo"}}, nil
}

func (c *genCloseCountingClient) CallTool(ctx context.Context, _ string, _ map[string]any) (CallToolResult, error) {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return CallToolResult{}, ctx.Err()
		}
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}

func (c *genCloseCountingClient) Close() error {
	c.closes.Add(1)
	if c.release != nil {
		select {
		case <-c.release:
		default:
			close(c.release)
		}
	}
	return nil
}

func TestConnCloseClosesCurrentAndRetired(t *testing.T) {
	oldGen := &genCloseCountingClient{}
	fresh := &genCloseCountingClient{}
	conn := newMCPServerConn(Server{Name: "s"}, oldGen)
	// Pin one call on the old generation so swap retires instead of closing.
	releaseCall := make(chan struct{})
	callDone := make(chan struct{})
	go func() {
		client, ok := conn.acquire()
		if !ok {
			close(callDone)
			return
		}
		defer conn.release(client)
		<-releaseCall
		close(callDone)
	}()
	// Wait until the call holds its reference.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		n := conn.inflight[oldGen]
		conn.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("call did not pin the old generation")
		}
	}
	conn.swap(fresh)
	if got := oldGen.closes.Load(); got != 0 {
		t.Fatalf("retired generation closed while in use: closes = %d, want 0", got)
	}
	close(releaseCall)
	<-callDone
	if got := oldGen.closes.Load(); got != 1 {
		t.Fatalf("retired generation closes = %d, want 1 after its last user", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fresh.closes.Load(); got != 1 {
		t.Fatalf("current generation closes = %d, want 1 after Close", got)
	}
	// Closed is terminal: second Close is a no-op and acquire refuses.
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, ok := conn.acquire(); ok {
		t.Fatal("acquire after Close must refuse")
	}
}

func TestConnCloseTerminatesStuckCall(t *testing.T) {
	stuck := &genCloseCountingClient{block: make(chan struct{})}
	conn := newMCPServerConn(Server{Name: "s"}, stuck)
	callDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := conn.call(ctx, "echo", map[string]any{})
		callDone <- err
	}()
	// Wait until the call pins the generation.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		n := len(conn.inflight)
		conn.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stuck call did not start")
		}
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- conn.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not terminate with a stuck call")
	}
	if got := stuck.closes.Load(); got != 1 {
		t.Fatalf("stuck generation closes = %d, want 1 after Close", got)
	}
	cancel()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("stuck call must fail after Close/cancel, not hang with success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stuck call did not return after Close/cancel")
	}
}
