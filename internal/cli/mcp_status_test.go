package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
)

func TestFormatMCPServerListShowsProbeStatus(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"up":       {Type: "http", URL: "http://127.0.0.1:1/mcp"},
		"down":     {Type: "http", URL: "http://127.0.0.1:1/mcp"},
		"off":      {Type: "http", URL: "http://127.0.0.1:1/mcp", Disabled: true},
		"proj":     {Type: "stdio", Command: "evil", ProjectConfigured: true},
		"unprobed": {Type: "http", URL: "http://127.0.0.1:1/mcp"},
	}
	probe := map[string]mcp.ProbeResult{
		"up":   {ToolCount: 2},
		"down": {Err: context.DeadlineExceeded},
		"off":  {Err: context.DeadlineExceeded},
	}
	output := formatMCPServerList(servers, probe, false)
	for _, want := range []string{"up", "✔ 2 tools", "down", "✘", "off", "desativado", "não verificado (projeto não confiável)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("formatMCPServerList() missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "unprobed · ✔") || strings.Contains(output, "unprobed · ✘") {
		t.Fatalf("formatMCPServerList() invented a status for an unprobed server:\n%s", output)
	}
}

func TestMCPProbeStatusMapStates(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"ok":   {Type: "http", URL: "http://127.0.0.1:1/mcp"},
		"bad":  {Type: "http", URL: "http://127.0.0.1:1/mcp"},
		"off":  {Type: "http", URL: "http://127.0.0.1:1/mcp", Disabled: true},
		"proj": {Type: "stdio", Command: "evil", ProjectConfigured: true},
	}
	probe := map[string]mcp.ProbeResult{
		"ok":  {ToolCount: 3},
		"bad": {Err: context.DeadlineExceeded},
	}
	status := mcpProbeStatusMap(servers, probe, false)
	if status["ok"].State != "ok" || status["ok"].Tools != 3 {
		t.Fatalf("ok = %+v", status["ok"])
	}
	if status["bad"].State != "unreachable" || status["bad"].Error == "" {
		t.Fatalf("bad = %+v, want unreachable with reason", status["bad"])
	}
	if status["off"].State != "disabled" {
		t.Fatalf("off = %+v, want disabled", status["off"])
	}
	if status["proj"].State != "unverified" {
		t.Fatalf("proj = %+v, want unverified", status["proj"])
	}
}

// `mcp list` probes a live server and reports its tool count.
func TestRunMCPListProbesLiveServer(t *testing.T) {
	t.Setenv("MCP_TIMEOUT", "5000")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var incoming struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&incoming); err != nil {
			t.Error(err)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		respond := func(result any) {
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": incoming.ID, "result": result})
		}
		switch incoming.Method {
		case "initialize":
			respond(map[string]any{"protocolVersion": "2024-11-05"})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			respond(map[string]any{"tools": []any{map[string]any{"name": "echo"}}})
		default:
			respond(map[string]any{})
		}
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"mcp", "list"}, &stdout, &stderr, appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"live": {Type: "http", URL: upstream.URL},
				"dead": {Type: "http", URL: "http://127.0.0.1:1/mcp"},
			}}, nil
		},
	})
	if exitCode != exitSuccess {
		t.Fatalf("exitCode = %d stderr=%s", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "live") || !strings.Contains(output, "✔ 1 tool") {
		t.Fatalf("mcp list did not report the live server:\n%s", output)
	}
	if !strings.Contains(output, "dead") || !strings.Contains(output, "✘") {
		t.Fatalf("mcp list did not report the dead server with a reason:\n%s", output)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDeps([]string{"mcp", "list", "--json"}, &stdout, &stderr, appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"live": {Type: "http", URL: upstream.URL},
			}}, nil
		},
	})
	if exitCode != exitSuccess {
		t.Fatalf("exitCode = %d stderr=%s", exitCode, stderr.String())
	}
	var payload struct {
		Status map[string]struct {
			State string `json:"state"`
			Tools int    `json:"tools"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	entry, ok := payload.Status["live"]
	if !ok || entry.State != "ok" || entry.Tools != 1 {
		t.Fatalf("status[live] = %+v, want ok with 1 tool", entry)
	}
}
