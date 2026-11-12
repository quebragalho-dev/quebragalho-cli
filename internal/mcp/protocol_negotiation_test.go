package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// The client announces the newest version it implements.
func TestNegotiateProtocolVersion(t *testing.T) {
	if got := latestMCPProtocolVersion(); got != "2025-06-18" {
		t.Fatalf("latestMCPProtocolVersion() = %q", got)
	}
	used, err := negotiateMCPProtocolVersion("2025-03-26")
	if err != nil || used != "2025-03-26" {
		t.Fatalf("negotiate(2025-03-26) = %q, %v; want the known version", used, err)
	}
	used, err = negotiateMCPProtocolVersion("2024-11-05")
	if err != nil || used != "2024-11-05" {
		t.Fatalf("negotiate(2024-11-05) = %q, %v; older known versions stay accepted", used, err)
	}
	if _, err := negotiateMCPProtocolVersion("2099-01-01"); err == nil {
		t.Fatal("negotiate(2099-01-01) error = nil, want refusal")
	} else if !strings.Contains(err.Error(), "2099-01-01") {
		t.Fatalf("refusal error = %q, want the demanded version named", err.Error())
	}
	// A server that will not say which revision it speaks is refused too:
	// connecting without a protocolVersion would leave the wire undefined.
	for _, missing := range []string{"", "   "} {
		if _, err := negotiateMCPProtocolVersion(missing); err == nil {
			t.Fatalf("negotiate(%q) error = nil, want refusal of a missing version", missing)
		} else if !strings.Contains(err.Error(), "protocolVersion") {
			t.Fatalf("refusal error = %q, want the missing protocolVersion named", err.Error())
		}
	}
	for _, garbage := range []string{"1999-01-01", "not-a-version"} {
		if _, err := negotiateMCPProtocolVersion(garbage); err == nil {
			t.Fatalf("negotiate(%q) error = nil, want refusal of unknown versions", garbage)
		}
	}
}

// A server demanding a newer revision than qg implements is not connected:
// its skipped reason names the version instead of pretending to speak it.
func TestNewerProtocolVersionRefusesWithReason(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		message := readHTTPRPCMessage(t, request)
		switch message.Method {
		case "initialize":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"protocolVersion": "2099-01-01"})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		default:
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{})
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(ctx, registry,
		config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"future": {Type: "http", URL: upstream.URL},
		}},
		RegisterOptions{AdvertiseInAuto: true})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	found := false
	for _, skipped := range runtime.Skipped() {
		if skipped.Name != "future" {
			continue
		}
		found = true
		if skipped.Err == nil || !strings.Contains(skipped.Err.Error(), "2099-01-01") {
			t.Fatalf("skipped reason = %v, want the demanded version named", skipped.Err)
		}
	}
	if !found {
		t.Fatal("future server was connected despite demanding protocol 2099-01-01")
	}
	if _, ok := registry.Get("mcp_future_future"); ok {
		t.Fatal("future server tools were registered despite the refused version")
	}
}
