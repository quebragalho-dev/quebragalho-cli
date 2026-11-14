package cli

// Acceptance tests for MCP servers that are down when qg starts. Startup makes
// one attempt per server and never waits out connect backoff: `qg exec` sends
// its first provider request and the interactive start hands over to the TUI
// right away. A server that was down keeps retrying in the background, and its
// tools join a following turn once it is back. The status keeps naming the
// server's real error.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

const (
	// startupSlack is all a server that is down may add to startup: one
	// connect attempt, never a backoff wait.
	startupSlack = 500 * time.Millisecond
	// comeBackBound is how long a server that comes back after startup may
	// take to show its tools: the background retry's first waits are 1s and
	// 2s, so this leaves room for several attempts.
	comeBackBound = 10 * time.Second
	// remoteTool is the tool the MCP backend serves, as qg names it for a
	// server configured as "remote".
	remoteTool = "mcp_remote_lookup"
)

// downAddr reserves a loopback address nothing listens on, so every dial to it
// is refused until a test starts serving there.
func downAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// serveMCPAt starts a working MCP server (one tool, "lookup") on addr.
func serveMCPAt(t *testing.T, addr string) {
	t.Helper()
	backend, _ := newAcceptanceCLIHTTPServer(t, http.StatusOK)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Errorf("listen again on %s: %v", addr, err)
		return
	}
	server := &http.Server{Handler: backend.Config.Handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// downServerURL is an MCP endpoint that is down in the given way: "refused"
// (nothing listens) or "503" (the server answers every request with 503).
func downServerURL(t *testing.T, way string) string {
	t.Helper()
	if way == "refused" {
		return "http://" + downAddr(t) + "/mcp"
	}
	server, _ := newAcceptanceCLIHTTPServer(t, http.StatusServiceUnavailable)
	return server.URL
}

// downServerError is the text the real error carries for each way down.
func downServerError(way string) string {
	if way == "refused" {
		return "connection refused"
	}
	return "503"
}

func writeUserMCPServers(t *testing.T, servers string) {
	t.Helper()
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":`+servers+`}}`)
}

func providerConfig(baseURL string) func(string, config.Overrides) (config.ResolvedConfig, error) {
	return func(string, config.Overrides) (config.ResolvedConfig, error) {
		return config.ResolvedConfig{
			ActiveProvider: "accept",
			Provider: config.ProviderProfile{
				Name:         "accept",
				ProviderKind: config.ProviderKindOpenAICompatible,
				BaseURL:      baseURL,
				APIKey:       "sk-accept",
				Model:        "m",
			},
			MaxTurns: 60,
		}, nil
	}
}

const (
	sseDone    = "data: [DONE]\n\n"
	sseAnswer  = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}` + "\n\n"
	sseListDir = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_%d","type":"function","function":{"name":"list_directory","arguments":"{\"path\":\".\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
)

// firstProviderRequest runs `qg exec hello` against an OpenAI-compatible stub
// that answers at once, and reports how long after the start the stub got
// its first request. register, when set, stands in for the MCP registration.
func firstProviderRequest(t *testing.T, register func(context.Context, *tools.Registry, config.MCPConfig, mcp.RegisterOptions) (mcpToolRuntime, error)) time.Duration {
	t.Helper()
	var once sync.Once
	first := make(chan time.Time, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { first <- time.Now() })
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseAnswer+sseDone)
	}))
	defer stub.Close()
	cwd := t.TempDir()

	start := time.Now()
	code := runWithDeps([]string{"exec", "hello"}, io.Discard, io.Discard, appDeps{
		getwd:            func() (string, error) { return cwd, nil },
		resolveConfig:    providerConfig(stub.URL + "/v1"),
		registerMCPTools: register,
	})
	select {
	case at := <-first:
		return at.Sub(start)
	default:
		t.Fatalf("qg exec exited %d without ever reaching the provider", code)
		return 0
	}
}

// redirectDefaultExa registers MCP servers for real, after pointing the
// built-in Exa default (still unconfigured) at target, so a test can make the
// default unreachable without leaving the machine. It counts the redirects.
func redirectDefaultExa(target string, redirected *atomic.Int32) func(context.Context, *tools.Registry, config.MCPConfig, mcp.RegisterOptions) (mcpToolRuntime, error) {
	return func(ctx context.Context, registry *tools.Registry, cfg config.MCPConfig, options mcp.RegisterOptions) (mcpToolRuntime, error) {
		servers := make(map[string]config.MCPServerConfig, len(cfg.Servers))
		for name, server := range cfg.Servers {
			if config.IsUnconfiguredDefault(name, server) {
				server.URL = target
				redirected.Add(1)
			}
			servers[name] = server
		}
		cfg.Servers = servers
		return mcp.RegisterTools(ctx, registry, cfg, options)
	}
}

// `qg exec` sends its first provider request without waiting on an MCP server
// that is down, whether the user configured it or it is the built-in Exa
// default: one attempt, then the run goes on.
func TestAcceptanceExecNeverWaitsOnAnMCPServerThatIsDown(t *testing.T) {
	for _, way := range []string{"refused", "503"} {
		t.Run("configured server "+way, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			base := firstProviderRequest(t, nil)
			writeUserMCPServers(t, `{"exa":{"disabled":true},"remote":{"type":"http","url":"`+downServerURL(t, way)+`"}}`)
			if waited := firstProviderRequest(t, nil); waited > base+startupSlack {
				t.Errorf("qg exec with a configured MCP server that is down (%s) sent its first provider request after %v, want at most %v (no MCP server: %v): startup must not wait out connect backoff", way, waited.Round(10*time.Millisecond), (base + startupSlack).Round(10*time.Millisecond), base.Round(10*time.Millisecond))
			}
		})
		t.Run("default Exa "+way, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			base := firstProviderRequest(t, nil)
			writeAcceptanceJSON(t, mustUserConfigPath(t), `{}`)
			var redirected atomic.Int32
			waited := firstProviderRequest(t, redirectDefaultExa(downServerURL(t, way), &redirected))
			if redirected.Load() == 0 {
				t.Fatal("the built-in Exa default was never registered, so this run proves nothing")
			}
			if waited > base+startupSlack {
				t.Errorf("qg exec with the default Exa server down (%s) sent its first provider request after %v, want at most %v (no MCP server: %v): startup must not wait out connect backoff", way, waited.Round(10*time.Millisecond), (base + startupSlack).Round(10*time.Millisecond), base.Round(10*time.Millisecond))
			}
		})
	}
}

// The interactive start hands over to the TUI (its first frame) without
// waiting on a configured MCP server that is down, and the MCP status the
// TUI shows already names the server's real error.
func TestAcceptanceInteractiveStartNeverWaitsOnAConfiguredMCPServerThatIsDown(t *testing.T) {
	for _, way := range []string{"refused", "503"} {
		t.Run(way, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			writeUserMCPServers(t, `{"exa":{"disabled":true},"remote":{"type":"http","url":"`+downServerURL(t, way)+`"}}`)
			cwd := t.TempDir()

			var opened time.Time
			reason := ""
			start := time.Now()
			code := runWithDeps([]string{}, io.Discard, io.Discard, appDeps{
				getwd:         func() (string, error) { return cwd, nil },
				resolveConfig: providerConfig("http://127.0.0.1:9/v1"),
				runTUI: func(_ context.Context, options tui.Options) int {
					opened = time.Now()
					reason = options.MCPSkipped["remote"]
					if options.MCPStatus != nil {
						for _, status := range options.MCPStatus() {
							if status.Name == "remote" && status.Reason != "" {
								reason = status.Reason
							}
						}
					}
					return exitSuccess
				},
			})
			if code != exitSuccess || opened.IsZero() {
				t.Fatalf("interactive start exit = %d, TUI opened = %v", code, !opened.IsZero())
			}
			if waited := opened.Sub(start); waited > startupSlack {
				t.Errorf("interactive start with a configured MCP server that is down (%s) opened the TUI after %v, want at most %v: startup must not wait out connect backoff", way, waited.Round(10*time.Millisecond), startupSlack)
			}
			if !strings.Contains(reason, downServerError(way)) {
				t.Errorf("MCP status for the server at TUI open = %q, want its real error (%q), not a generic one", reason, downServerError(way))
			}
		})
	}
}

// A configured server that is down at startup and comes back while the TUI is
// open connects on its own: its tools land in the registry the TUI takes each
// turn from, without a restart or a manual reconnect.
func TestAcceptanceInteractiveSessionPicksUpAServerThatComesBackLater(t *testing.T) {
	acceptanceHermeticEnv(t)
	addr := downAddr(t)
	writeUserMCPServers(t, `{"exa":{"disabled":true},"remote":{"type":"http","url":"http://`+addr+`/mcp"}}`)
	cwd := t.TempDir()

	var opened, joined time.Time
	start := time.Now()
	code := runWithDeps([]string{}, io.Discard, io.Discard, appDeps{
		getwd:         func() (string, error) { return cwd, nil },
		resolveConfig: providerConfig("http://127.0.0.1:9/v1"),
		runTUI: func(_ context.Context, options tui.Options) int {
			opened = time.Now()
			serveMCPAt(t, addr)
			for deadline := time.Now().Add(comeBackBound); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				if _, ok := options.Registry.Get(remoteTool); ok {
					joined = time.Now()
					break
				}
			}
			return exitSuccess
		},
	})
	if code != exitSuccess || opened.IsZero() {
		t.Fatalf("interactive start exit = %d, TUI opened = %v", code, !opened.IsZero())
	}
	if waited := opened.Sub(start); waited > startupSlack {
		t.Errorf("interactive start with a configured MCP server that is down opened the TUI after %v, want at most %v", waited.Round(10*time.Millisecond), startupSlack)
	}
	if joined.IsZero() {
		t.Errorf("the server came back right after the TUI opened, but %s never joined the session's tools within %v: a server down at startup must keep retrying in the background", remoteTool, comeBackBound)
	}
}

// `qg exec` over several turns: a configured server that is down at startup
// and comes back once the run is under way connects on its own, and its tools
// are offered to the model in a following turn.
func TestAcceptanceExecPicksUpAServerThatComesBackInAFollowingTurn(t *testing.T) {
	acceptanceHermeticEnv(t)
	addr := downAddr(t)
	writeUserMCPServers(t, `{"exa":{"disabled":true},"remote":{"type":"http","url":"http://`+addr+`/mcp"}}`)

	const turnPause = 250 * time.Millisecond
	maxRequests := int(comeBackBound / turnPause)
	var mu sync.Mutex
	requests := 0
	joinedAt := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests++
		n := requests
		if n == 1 {
			// The run is under way: the server comes back now.
			serveMCPAt(t, addr)
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == remoteTool && joinedAt == 0 {
				joinedAt = n
			}
		}
		done := joinedAt != 0 || n >= maxRequests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if done {
			_, _ = io.WriteString(w, sseAnswer+sseDone)
			return
		}
		time.Sleep(turnPause)
		_, _ = io.WriteString(w, fmt.Sprintf(sseListDir, n)+sseDone)
	}))
	defer stub.Close()
	cwd := t.TempDir()

	code := runWithDeps([]string{"exec", "--auto", "high", "hello"}, io.Discard, io.Discard, appDeps{
		getwd:         func() (string, error) { return cwd, nil },
		resolveConfig: providerConfig(stub.URL + "/v1"),
	})
	mu.Lock()
	defer mu.Unlock()
	if requests < 2 {
		t.Fatalf("qg exec exited %d after %d provider requests, want a run over several turns", code, requests)
	}
	if joinedAt == 0 {
		t.Errorf("the server came back during the first turn, but %s was offered in none of the %d requests that followed (%v of turns): a server down at startup must keep retrying in the background and join a following turn", remoteTool, requests-1, comeBackBound)
	}
}
