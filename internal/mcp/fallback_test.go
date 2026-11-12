package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// sseOnlyServer is a legacy-SSE MCP server: POST /mcp 404s (it only speaks
// the old GET-stream + POST-endpoint dialect).
type sseOnlyServer struct {
	t         *testing.T
	responses chan string
}

func (fake *sseOnlyServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintf(writer, "event: endpoint\ndata: /msg\n\n")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		for {
			select {
			case <-request.Context().Done():
				return
			case response := <-fake.responses:
				fmt.Fprintf(writer, "event: message\ndata: %s\n\n", response)
				if flusher, ok := writer.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	})
	mux.HandleFunc("/msg", func(writer http.ResponseWriter, request *http.Request) {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
			fake.t.Error(err)
			return
		}
		answer := func(result any) {
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
			fake.responses <- string(raw)
		}
		switch message.Method {
		case "initialize":
			answer(map[string]any{"protocolVersion": "2024-11-05"})
		case "tools/list":
			answer(map[string]any{"tools": []any{map[string]any{"name": "legacy"}}})
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	})
	return mux
}

func TestImplicitHTTPFallsBackToSSE(t *testing.T) {
	fake := &sseOnlyServer{t: t, responses: make(chan string, 8)}
	upstream := httptest.NewServer(fake.handler())
	defer upstream.Close()

	servers, err := NormalizeConfig(config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"sseonly": {URL: upstream.URL + "/mcp"},
	}})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if !servers[0].TypeImplicit {
		t.Fatal("omitted type must mark the server as implicit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := Connect(ctx, servers[0])
	if err != nil {
		t.Fatalf("Connect() error = %v, want SSE fallback", err)
	}
	defer client.Close()
	remote, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(remote) != 1 || remote[0].Name != "legacy" {
		t.Fatalf("ListTools() = %+v, want the legacy tool", remote)
	}
}

// An explicit http server that answers 404 stays strict: no silent fallback.
func TestExplicitHTTPDoesNotFallBackToSSE(t *testing.T) {
	fake := &sseOnlyServer{t: t, responses: make(chan string, 8)}
	upstream := httptest.NewServer(fake.handler())
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := Connect(ctx, Server{Name: "strict", Type: ServerTypeHTTP, URL: upstream.URL + "/mcp"})
	if err == nil {
		_ = client.Close()
		t.Fatal("Connect() error = nil, want strict failure for explicit http")
	}
}

func TestSSEFallbackErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"not found", &MCPHTTPStatusError{Status: 404}, true},
		{"method not allowed", &MCPHTTPStatusError{Status: 405}, true},
		{"not acceptable", &MCPHTTPStatusError{Status: 406}, true},
		{"unauthorized", &MCPHTTPStatusError{Status: 401}, false},
		{"server error", &MCPHTTPStatusError{Status: 500}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSEFallbackError(tc.err); got != tc.want {
				t.Fatalf("isSSEFallbackError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
