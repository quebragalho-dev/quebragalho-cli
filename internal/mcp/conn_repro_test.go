package mcp

import (
	"context"
	"sync/atomic"
	"testing"
)

// blockingCallClient mimics a real ToolClient whose in-flight CallTool is
// failed by Close (failAll): once Close runs, the blocked call observes it
// and returns errMCPClientClosed instead of the server answer.
type blockingCallClient struct {
	entered  chan struct{}
	release  chan struct{}
	closed   atomic.Int32
	enteredN atomic.Int32
}

func newBlockingCallClient() *blockingCallClient {
	return &blockingCallClient{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *blockingCallClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "echo"}}, nil
}

func (c *blockingCallClient) CallTool(ctx context.Context, _ string, _ map[string]any) (CallToolResult, error) {
	if c.enteredN.Add(1) == 1 {
		close(c.entered)
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return CallToolResult{}, ctx.Err()
	}
	if c.closed.Load() == 1 {
		return CallToolResult{}, errMCPClientClosed
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}

func (c *blockingCallClient) Close() error {
	c.closed.Store(1)
	return nil
}

// A swap under an in-flight call must not drop the server answer: the old
// generation stays alive until its users finish.
func TestConnSwapKeepsInFlightCall(t *testing.T) {
	oldGen := newBlockingCallClient()
	fresh := &scriptedCallClient{}
	conn := newMCPServerConn(Server{Name: "s"}, oldGen)

	type outcome struct {
		result CallToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := conn.call(context.Background(), "echo", map[string]any{})
		done <- outcome{result, err}
	}()

	<-oldGen.entered
	conn.swap(fresh)
	close(oldGen.release)

	out := <-done
	if out.err != nil {
		t.Fatalf("in-flight call on the old generation failed after swap: %v (want the server answer)", out.err)
	}
	if len(out.result.Content) != 1 || out.result.Content[0].Text != "ok" {
		t.Fatalf("in-flight call result = %+v, want the server answer", out.result)
	}
}
