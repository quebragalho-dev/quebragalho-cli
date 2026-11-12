package mcp

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// TestMCPStdioListChangedHelperProcess is a stdio MCP server that emits one
// unsolicited notifications/tools/list_changed right after initialize.
func TestMCPStdioListChangedHelperProcess(t *testing.T) {
	if os.Getenv("QG_MCP_STDIO_LIST_CHANGED_HELPER") != "1" {
		return
	}
	reader := newMessageReader(os.Stdin)
	writer := newMessageWriter(os.Stdout)
	for {
		message, err := reader.read()
		if err != nil {
			return
		}
		if message.Method == "notifications/initialized" {
			continue
		}
		switch message.Method {
		case "initialize":
			_ = writer.write(rpcMessage{
				JSONRPC: "2.0",
				ID:      message.ID,
				Result: mustRaw(map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "changing", "version": "1.0.0"},
				}),
			})
			_ = writer.write(rpcMessage{
				JSONRPC: "2.0",
				Method:  "notifications/tools/list_changed",
				Params:  mustRaw(map[string]any{}),
			})
		case "tools/list":
			_ = writer.write(rpcMessage{
				JSONRPC: "2.0",
				ID:      message.ID,
				Result:  mustRaw(map[string]any{"tools": []any{}}),
			})
		default:
			if message.ID == nil {
				continue
			}
			_ = writer.write(rpcMessage{
				JSONRPC: "2.0",
				ID:      message.ID,
				Error:   &rpcError{Code: -32601, Message: "method not found"},
			})
		}
	}
}

// An unsolicited tools/list_changed notification must mark the client stale
// so the catalog refreshes on the next turn instead of mid-request.
func TestStdioListChangedMarksClientStale(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, Server{
		Name:    "changing",
		Type:    ServerTypeStdio,
		Command: executable,
		Args:    []string{"-test.run=TestMCPStdioListChangedHelperProcess", "--"},
		Env:     map[string]string{"QG_MCP_STDIO_LIST_CHANGED_HELPER": "1"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	staler, ok := client.(staleNotifier)
	if !ok {
		t.Fatal("stdio client does not expose tools-list staleness")
	}
	select {
	case <-staler.ToolsStale():
	case <-time.After(5 * time.Second):
		t.Fatal("tools/list_changed did not mark the client stale")
	}
}

type evolvingFakeClient struct {
	mu     sync.Mutex
	listed []RemoteTool
	stale  chan struct{}
	closed int
}

func (client *evolvingFakeClient) ListTools(context.Context) ([]RemoteTool, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]RemoteTool{}, client.listed...), nil
}

func (client *evolvingFakeClient) CallTool(_ context.Context, _ string, _ map[string]any) (CallToolResult, error) {
	return CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}

func (client *evolvingFakeClient) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.closed++
	return nil
}

func (client *evolvingFakeClient) ToolsStale() <-chan struct{} { return client.stale }

func (client *evolvingFakeClient) markToolsStale() {
	select {
	case client.stale <- struct{}{}:
	default:
	}
}

// A stale server re-lists on the next turn boundary: new tools appear,
// vanished tools leave, and the change is ledgered as "ferramentas mudaram"
// for cache accounting.
func TestRefreshIfStaleUpdatesCatalogBetweenTurns(t *testing.T) {
	fake := &evolvingFakeClient{
		listed: []RemoteTool{{Name: "v1"}},
		stale:  make(chan struct{}, 1),
	}
	factory := func(context.Context, Server) (ToolClient, error) { return fake, nil }

	ctx := context.Background()
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(ctx, registry,
		config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"changing": {Type: "http", URL: "http://127.0.0.1:1/mcp"},
		}},
		RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if _, ok := registry.Get("mcp_changing_v1"); !ok {
		t.Fatal("v1 tool was not registered")
	}

	fake.mu.Lock()
	fake.listed = []RemoteTool{{Name: "v2"}}
	fake.mu.Unlock()
	fake.stale <- struct{}{}

	runtime.RefreshPendingTools(ctx)
	if _, ok := registry.Get("mcp_changing_v2"); !ok {
		t.Fatal("v2 tool was not registered after list_changed")
	}
	if _, ok := registry.Get("mcp_changing_v1"); ok {
		t.Fatal("v1 tool still registered after the server dropped it")
	}
	changes := runtime.CatalogChanges()
	if len(changes) != 1 || changes[0].Reason != "ferramentas mudaram" {
		t.Fatalf("CatalogChanges() = %+v, want one 'ferramentas mudaram' entry", changes)
	}
}

// The refresh runs from the tool Run preamble, so the current turn keeps its
// already-computed partition and only the next turn sees the new catalog.
func TestRunInvokesRefreshCheck(t *testing.T) {
	checked := make(chan struct{}, 1)
	tool := newRegistryTool(
		Server{Name: "s", Type: ServerTypeHTTP},
		RemoteTool{Name: "echo"},
		&fakeToolClient{listed: []RemoteTool{{Name: "echo"}}},
		RegisterOptions{},
	)
	tool.refreshCheck = func(context.Context) {
		checked <- struct{}{}
	}
	tool.Run(context.Background(), map[string]any{"query": "hi"})
	select {
	case <-checked:
	default:
		t.Fatal("Run() did not invoke the refresh check")
	}
}
