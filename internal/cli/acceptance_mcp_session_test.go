package cli

// MCP acceptance tests through the real app: config layering, CLI output and
// the interactive session the TUI drives. The TUI is replaced by a function
// that uses tui.Options the way the TUI does.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

const (
	sessionHelperModeEnv  = "QG_ACCEPT_SESSION_MODE"
	sessionHelperStateEnv = "QG_ACCEPT_SESSION_STATE"
)

// acceptanceSessionServerJSON is a stdio server entry running this test binary
// in one mode of TestAcceptanceSessionHelperProcess.
func acceptanceSessionServerJSON(t *testing.T, mode string, state string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(map[string]string{sessionHelperModeEnv: mode, sessionHelperStateEnv: state})
	return fmt.Sprintf(`{"type":"stdio","command":%q,"args":["-test.run=^TestAcceptanceSessionHelperProcess$"],"env":%s}`, binary, env)
}

// acceptanceInteractive starts the interactive app in repo with the default
// deps (plus any set in deps) and runs session where the TUI would run.
func acceptanceInteractive(t *testing.T, repo string, deps appDeps, session func(ctx context.Context, options tui.Options)) string {
	t.Helper()
	var stderr bytes.Buffer
	deps.getwd = func() (string, error) { return repo, nil }
	deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
		return config.ResolvedConfig{Provider: config.ProviderProfile{
			Name:         "accept",
			ProviderKind: config.ProviderKindOpenAICompatible,
			BaseURL:      "http://127.0.0.1:9/v1",
			APIKey:       "sk-accept",
			Model:        "m",
		}}, nil
	}
	reached := false
	deps.runTUI = func(ctx context.Context, options tui.Options) int {
		reached = true
		session(ctx, options)
		return exitSuccess
	}
	if code := runWithDeps([]string{}, io.Discard, &stderr, deps); code != exitSuccess || !reached {
		t.Fatalf("interactive start exit = %d, reached TUI = %v: %s", code, reached, stderr.String())
	}
	return stderr.String()
}

func acceptanceCallTool(ctx context.Context, registry *tools.Registry, name string) tools.Result {
	return registry.RunWithOptions(ctx, name, map[string]any{}, tools.RunOptions{PermissionGranted: true})
}

// A trusted project may tune a user's server, but it cannot lift the user's
// disabled_tools: the user's deny stays in force, or the server needs a new
// approval.
func TestAcceptanceMCPProjectCannotLiftUserDisabledTools(t *testing.T) {
	acceptanceHermeticEnv(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	user := strings.TrimSuffix(acceptanceStdioServerJSON(t, marker, ""), "}") + `,"disabled_tools":["lookup"]}`
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},"u":`+user+`}}}`)

	repo := acceptanceRepo(t, `{"u":{"timeout":5000}}`, true)
	acceptanceApproveProjectServer(t, repo, "u")
	if _, visible := acceptanceRegisterWorkspace(t, repo).Get("mcp_u_lookup"); visible {
		t.Fatal("precondition: user-disabled tool visible with an approved timeout-only project override")
	}

	writeAcceptanceJSON(t, filepath.Join(repo, ".qg", "config.json"), `{"mcp":{"servers":{"u":{"timeout":5000,"disabled_tools":[]}}}}`)
	if _, visible := acceptanceRegisterWorkspace(t, repo).Get("mcp_u_lookup"); visible {
		t.Fatal(`project "disabled_tools":[] lifted the user's deny on mcp_u_lookup without a new approval`)
	}
}

// An expanded ${VAR} in a URL path is never printed by `qg mcp list` or
// `qg mcp check`, in text or JSON (net/http dial errors echo the full URL).
func TestAcceptanceMCPCLIExpandedSecretInURLPathIsNeverPrinted(t *testing.T) {
	acceptanceHermeticEnv(t)
	const secret = "sk-path-secret-4242"
	t.Setenv("QG_ACCEPT_SECRET", secret)
	t.Setenv("MCP_TIMEOUT", "2000")
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},`+
		`"remote":{"type":"http","url":"http://127.0.0.1:1/${QG_ACCEPT_SECRET}/mcp"}}}}`)
	repo := acceptanceRepo(t, `{}`, true)

	leaks := []string{}
	for _, args := range [][]string{{"mcp", "list"}, {"mcp", "list", "--json"}, {"mcp", "check", "remote"}, {"mcp", "check", "remote", "--json"}} {
		stdout, stderr, _ := acceptanceRunCLI(t, repo, args...)
		output := stdout + stderr
		if !strings.Contains(output, "remote") {
			t.Fatalf("qg %s does not report server remote:\n%s", strings.Join(args, " "), output)
		}
		if strings.Contains(output, secret) {
			leaks = append(leaks, "qg "+strings.Join(args, " ")+":\n"+output)
		}
	}
	if len(leaks) > 0 {
		t.Fatalf("expanded URL-path secret printed by %d command(s):\n%s", len(leaks), strings.Join(leaks, "\n"))
	}
}

// An approved project server is probed by `qg mcp list` whether or not its
// config uses ${VAR}: approval binds the config as written, before expansion.
func TestAcceptanceMCPListProbesApprovedProjectServerWithVars(t *testing.T) {
	for name, extraEnv := range map[string]string{"literal": "", "with-var": `"ACCEPT_TOKEN":"${QG_ACCEPT_TOKEN}"`} {
		t.Run(name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			t.Setenv("QG_ACCEPT_TOKEN", "v")
			marker := filepath.Join(t.TempDir(), "spawned")
			repo := acceptanceRepo(t, `{"proj":`+acceptanceStdioServerJSON(t, marker, extraEnv)+`}`, true)
			acceptanceApproveProjectServer(t, repo, "proj")

			stdout, stderr, _ := acceptanceRunCLI(t, repo, "mcp", "list")
			if strings.Contains(stdout+stderr, "aguarda") || !acceptanceSpawned(marker) {
				t.Fatalf("approved project server proj was not probed (spawned=%v):\n%s%s", acceptanceSpawned(marker), stdout, stderr)
			}
		})
	}
}

// acceptanceToolOnceProvider is a model that calls tool once and answers
// after it gets the result back, which it keeps.
type acceptanceToolOnceProvider struct {
	tool    string
	mu      sync.Mutex
	sent    bool
	output  string
	isError bool
}

func (p *acceptanceToolOnceProvider) StreamCompletion(_ context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	events := make(chan qgruntime.StreamEvent, 4)
	defer close(events)
	for _, message := range request.Messages {
		if message.Role == qgruntime.MessageRoleTool {
			p.mu.Lock()
			p.sent, p.output, p.isError = true, message.Content, message.IsError
			p.mu.Unlock()
			events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "done"}
			events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventDone}
			return events, nil
		}
	}
	events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "c1", ToolName: p.tool}
	events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "c1", ArgumentsFragment: "{}"}
	events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "c1"}
	events <- qgruntime.StreamEvent{Type: qgruntime.StreamEventDone}
	return events, nil
}

func (p *acceptanceToolOnceProvider) toolResult() (sent bool, output string, isError bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent, p.output, p.isError
}

// A tools/list_changed seen during one turn is applied by the time the next
// turn starts. Turn 1 runs the real agent loop with the options the app hands
// the TUI; only the permission mode changes, so the call needs no prompt.
// The TUI waits for tool readiness before every turn reads the catalog
// (TestPromptWaitsForToolReadinessBeforeSnapshot).
func TestAcceptanceMCPListChangedReachesTheNextTurn(t *testing.T) {
	acceptanceHermeticEnv(t)
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},"s":`+
		acceptanceSessionServerJSON(t, "list-changed", t.TempDir())+`}}}`)
	repo := acceptanceRepo(t, `{}`, true)

	acceptanceInteractive(t, repo, appDeps{}, func(ctx context.Context, options tui.Options) {
		startTurn := func() {
			if options.AwaitToolReadiness != nil {
				options.AwaitToolReadiness(ctx)
			}
		}
		startTurn()
		turn := options.AgentOptions
		turn.PermissionMode = agent.PermissionModeUnsafe
		model := &acceptanceToolOnceProvider{tool: "mcp_s_lookup"}
		if _, err := agent.Run(ctx, "use the tool", model, turn); err != nil {
			t.Fatalf("turn 1: %v", err)
		}
		if sent, output, isError := model.toolResult(); !sent || isError || !strings.Contains(output, "ok") {
			t.Fatalf("precondition: turn 1 call to mcp_s_lookup: sent=%v error=%v output=%q", sent, isError, output)
		}
		startTurn()
		if _, ok := options.Registry.Get("mcp_s_fresh"); !ok {
			t.Fatal("tool announced by tools/list_changed in turn 1 is missing from the catalog when turn 2 starts")
		}
	})
}

// TestAcceptanceSessionHelperProcess is not a test: it is a stdio MCP
// server and returns at once unless spawned with QG_ACCEPT_SESSION_MODE. Each
// start appends a line to <state>/starts.
//
// Modes:
//   - "basic": one tool "lookup".
//   - "list-changed": "lookup" until the first call, which announces
//     tools/list_changed before answering; from then on "lookup" and "fresh".
//   - "fail-first": the first process exits before initialize; later
//     processes behave like "basic".
func TestAcceptanceSessionHelperProcess(t *testing.T) {
	mode := os.Getenv(sessionHelperModeEnv)
	if mode == "" {
		return
	}
	startsFile := filepath.Join(os.Getenv(sessionHelperStateEnv), "starts")
	previous, _ := os.ReadFile(startsFile)
	if file, err := os.OpenFile(startsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
		_ = file.Close()
	}
	if mode == "fail-first" && len(previous) == 0 {
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	write := func(message map[string]any) {
		body, _ := json.Marshal(message)
		_, _ = os.Stdout.Write(append(body, '\n'))
	}
	catalog := []string{"lookup"}
	for scanner.Scan() {
		var request struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			result = map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "accept-session", "version": "1"},
			}
		case "tools/list":
			listed := []map[string]any{}
			for _, name := range catalog {
				listed = append(listed, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": listed}
		case "tools/call":
			if mode == "list-changed" && len(catalog) == 1 {
				catalog = append(catalog, "fresh")
				write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}
