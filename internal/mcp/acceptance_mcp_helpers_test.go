package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// Hermetic environment shared by every MCP acceptance test: private HOME, XDG
// dirs and TMPDIR (the spill directory lives under os.TempDir), and the file
// credential backend so no test can reach the macOS Keychain. Nothing in this
// suite talks to a non-loopback address.
func acceptanceHermeticEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"xdg-config", "xdg-data", "xdg-state", "xdg-cache", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("QG_CRED_STORAGE", "file")
	for _, name := range []string{"MCP_TOOL_TIMEOUT", "MCP_TIMEOUT", "MAX_MCP_OUTPUT_TOKENS", "QG_TOOL_OUTPUT_CEILING_TOKENS"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	return root
}

// acceptanceConfig parses the `mcp` block exactly as a user writes it in
// config.json, so new keys (timeout, enabled_tools, disabled_tools) are tested
// through the real file contract instead of Go struct fields.
func acceptanceConfig(t *testing.T, servers string) config.MCPConfig {
	t.Helper()
	var cfg config.MCPConfig
	if err := json.Unmarshal([]byte(`{"servers":`+servers+`}`), &cfg); err != nil {
		t.Fatalf("parse MCP config: %v", err)
	}
	return cfg
}

func acceptanceRegister(t *testing.T, cfg config.MCPConfig) (*tools.Registry, *Runtime) {
	t.Helper()
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, cfg, RegisterOptions{})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return registry, runtime
}

func acceptanceSkippedReason(runtime *Runtime, server string) (string, bool) {
	for _, skipped := range runtime.Skipped() {
		if skipped.Name == server {
			if skipped.Err == nil {
				return "", true
			}
			return skipped.Err.Error(), true
		}
	}
	return "", false
}

// acceptanceRun calls a registered MCP tool the way the agent loop does and
// bounds the wait, so a hang fails the test instead of the suite.
func acceptanceRun(t *testing.T, registry *tools.Registry, name string, bound time.Duration) (tools.Result, bool) {
	t.Helper()
	done := make(chan tools.Result, 1)
	go func() {
		done <- registry.RunWithOptions(context.Background(), name, map[string]any{}, tools.RunOptions{PermissionGranted: true})
	}()
	select {
	case result := <-done:
		return result, true
	case <-time.After(bound):
		return tools.Result{}, false
	}
}

func registryHasTool(registry *tools.Registry, name string) bool {
	_, ok := registry.Get(name)
	return ok
}

func containsAnyFold(haystack string, needles ...string) bool {
	lower := strings.ToLower(haystack)
	for _, needle := range needles {
		if strings.Contains(lower, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Streamable HTTP server
// ---------------------------------------------------------------------------

type acceptanceHTTPServer struct {
	*httptest.Server
	release chan struct{}
	mu      sync.Mutex
	// announced is the protocolVersion of the latest initialize request.
	announced string
	// headers records the MCP-Protocol-Version header per method.
	headers map[string]string
}

func (server *acceptanceHTTPServer) announcedVersion() string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.announced
}

func (server *acceptanceHTTPServer) protocolHeader(method string) string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.headers[method]
}

// block parks a handler until the client gives up or the test ends; cleanup
// releases it so httptest.Server.Close never waits on a parked handler.
func (server *acceptanceHTTPServer) block(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-server.release:
	}
}

// newAcceptanceHTTPServer answers initialize (echoing the client's version),
// tools/list (one tool "lookup") and tools/call ("ok"). hook may take over any
// request by returning true.
func newAcceptanceHTTPServer(t *testing.T, hook func(server *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool) *acceptanceHTTPServer {
	t.Helper()
	server := &acceptanceHTTPServer{release: make(chan struct{}), headers: map[string]string{}}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := readHTTPRPCMessage(t, r)
		server.mu.Lock()
		server.headers[message.Method] = r.Header.Get("MCP-Protocol-Version")
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if message.Method == "initialize" {
			_ = json.Unmarshal(message.Params, &params)
			server.announced = params.ProtocolVersion
		}
		server.mu.Unlock()
		if hook != nil && hook(server, message, w, r) {
			return
		}
		switch message.Method {
		case "initialize":
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "accept-http", "version": "1"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"tools": []RemoteTool{{Name: "lookup"}}})
		case "tools/call":
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"content": []Content{{Type: "text", Text: "ok"}}})
		default:
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{})
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(server.release) }) // LIFO: runs before Close
	return server
}

func hijackClose(w http.ResponseWriter) {
	if hijacker, ok := w.(http.Hijacker); ok {
		if conn, _, err := hijacker.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

// ---------------------------------------------------------------------------
// Legacy HTTP+SSE server: GET /sse streams, POST /messages enqueues the
// response on the stream, POST /sse is refused like a real legacy server.
// ---------------------------------------------------------------------------

type acceptanceSSEServer struct {
	*httptest.Server
	release       chan struct{}
	streamOpened  atomic.Bool
	postsToStream atomic.Int32
}

func newAcceptanceSSEServer(t *testing.T, hangToolCall bool) *acceptanceSSEServer {
	t.Helper()
	server := &acceptanceSSEServer{release: make(chan struct{})}
	events := make(chan string, 16)
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sse" && r.Method == http.MethodGet:
			server.streamOpened.Store(true)
			flusher, _ := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
			flusher.Flush()
			for {
				select {
				case event := <-events:
					_, _ = fmt.Fprint(w, event)
					flusher.Flush()
				case <-r.Context().Done():
					return
				case <-server.release:
					return
				}
			}
		case r.URL.Path == "/sse":
			server.postsToStream.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == "/messages" && r.Method == http.MethodPost:
			message := readHTTPRPCMessage(t, r)
			switch message.Method {
			case "initialize":
				events <- formatSSERPCResponse(t, message.ID, map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "accept-sse", "version": "1"},
				})
			case "tools/list":
				events <- formatSSERPCResponse(t, message.ID, map[string]any{
					"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}},
				})
			case "tools/call":
				if !hangToolCall {
					events <- formatSSERPCResponse(t, message.ID, map[string]any{
						"content": []map[string]any{{"type": "text", "text": "ok"}},
					})
				}
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(server.release) })
	return server
}

// ---------------------------------------------------------------------------
// stdio server: this test binary re-executed with a mode. The mode and a state
// directory travel in the server's env block (Server.Env), which is exactly
// how a user-configured stdio server receives its environment.
// ---------------------------------------------------------------------------

const (
	acceptanceHelperModeEnv  = "QG_ACCEPT_HELPER_MODE"
	acceptanceHelperStateEnv = "QG_ACCEPT_HELPER_STATE"
)

type acceptanceStdio struct {
	binary string
	args   []string
	state  string
	mode   string
}

func newAcceptanceStdio(t *testing.T, mode string) acceptanceStdio {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	return acceptanceStdio{
		binary: binary,
		args:   []string{"-test.run=^TestAcceptanceStdioHelperProcess$"},
		state:  t.TempDir(),
		mode:   mode,
	}
}

// configJSON renders the server entry for acceptanceConfig.
func (helper acceptanceStdio) configJSON(extra string) string {
	args, _ := json.Marshal(helper.args)
	env, _ := json.Marshal(map[string]string{acceptanceHelperModeEnv: helper.mode, acceptanceHelperStateEnv: helper.state})
	entry := fmt.Sprintf(`{"type":"stdio","command":%q,"args":%s,"env":%s`, helper.binary, args, env)
	if extra != "" {
		entry += "," + extra
	}
	return entry + "}"
}

func (helper acceptanceStdio) starts() int {
	data, _ := os.ReadFile(filepath.Join(helper.state, "starts"))
	return strings.Count(string(data), "\n")
}

// observed returns the argv and selected env the spawned process really got.
func (helper acceptanceStdio) observed(t *testing.T) (args []string, env map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(helper.state, "observed.json"))
	if err != nil {
		t.Fatalf("stdio helper never started: %v", err)
	}
	var observed struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatalf("decode helper observation: %v", err)
	}
	return observed.Args, observed.Env
}

func (helper acceptanceStdio) announced() string {
	data, _ := os.ReadFile(filepath.Join(helper.state, "announced"))
	return strings.TrimSpace(string(data))
}

// TestAcceptanceStdioHelperProcess is not a test: it is the stdio MCP server
// process for the acceptance suite and returns immediately unless spawned
// with QG_ACCEPT_HELPER_MODE.
//
// Modes:
//   - "basic": one tool "lookup".
//   - "hang": tools/call never answers.
//   - "crash-once": the first process exits on tools/call without answering;
//     later processes answer normally (proves restart on next use).
//   - "list-changed": tools/call bumps the catalog to "lookup_v2" and sends
//     notifications/tools/list_changed before answering, i.e. mid-turn.
func TestAcceptanceStdioHelperProcess(t *testing.T) {
	mode := os.Getenv(acceptanceHelperModeEnv)
	if mode == "" {
		return
	}
	state := os.Getenv(acceptanceHelperStateEnv)
	startsFile := filepath.Join(state, "starts")
	previous, _ := os.ReadFile(startsFile)
	startNumber := strings.Count(string(previous), "\n") + 1
	appendLine(startsFile, strconv.Itoa(os.Getpid()))
	observed, _ := json.Marshal(map[string]any{
		"args": os.Args[1:],
		"env": map[string]string{
			"ACCEPT_A": os.Getenv("ACCEPT_A"),
			"ACCEPT_B": os.Getenv("ACCEPT_B"),
			"ACCEPT_C": os.Getenv("ACCEPT_C"),
		},
	})
	_ = os.WriteFile(filepath.Join(state, "observed.json"), observed, 0o600)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	write := func(message map[string]any) {
		body, _ := json.Marshal(message)
		_, _ = writer.Write(append(body, '\n'))
		_ = writer.Flush()
	}
	catalogVersion := 1
	for scanner.Scan() {
		var request struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			_ = os.WriteFile(filepath.Join(state, "announced"), []byte(params.ProtocolVersion), 0o600)
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "accept-stdio", "version": "1"},
			}})
		case "tools/list":
			name := "lookup"
			if catalogVersion > 1 {
				name = "lookup_v2"
			}
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"tools": []map[string]any{{"name": name, "description": "d", "inputSchema": map[string]any{"type": "object"}}},
			}})
		case "tools/call":
			switch {
			case mode == "hang":
				continue
			case mode == "crash-once" && startNumber == 1:
				os.Exit(3)
			case mode == "list-changed":
				catalogVersion = 2
				write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed", "params": map[string]any{}})
			}
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok"}},
			}})
		default:
			if request.ID != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
			}
		}
	}
	os.Exit(0)
}

func appendLine(path string, line string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line + "\n")
}
