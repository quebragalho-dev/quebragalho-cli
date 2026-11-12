package mcp

// protocolVersion on both sides: `qg serve --mcp` answers a client in the
// version it asked for when qg knows it, and the qg client refuses, with the
// version in the reason, any server answer it does not know.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptanceServeInitialize(t *testing.T, requested string) string {
	t.Helper()
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": requested,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "accept-client", "version": "1"},
	})
	var input, output bytes.Buffer
	if err := newMessageWriter(&input).write(rpcMessage{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: params}); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), &input, &output, tools.NewRegistry(), ServeOptions{Name: "qg", Version: "test"}); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	reply, err := newMessageReader(&output).read()
	if err != nil {
		t.Fatalf("read initialize reply: %v", err)
	}
	if reply.Error != nil {
		t.Fatalf("initialize %s answered with error %#v", requested, reply.Error)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		t.Fatalf("decode initialize result: %v\n%s", err, reply.Result)
	}
	return result.ProtocolVersion
}

var acceptanceServeKnownVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

func TestAcceptanceMCPServeAnswersTheRequestedKnownVersion(t *testing.T) {
	for _, requested := range acceptanceServeKnownVersions {
		t.Run(requested, func(t *testing.T) {
			if got := acceptanceServeInitialize(t, requested); got != requested {
				t.Fatalf("client asked for %s, qg serve --mcp answered %s", requested, got)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		if got := acceptanceServeInitialize(t, "2099-01-01"); !slices.Contains(acceptanceServeKnownVersions, got) {
			t.Fatalf("client asked for unknown 2099-01-01, qg serve --mcp answered %q, want a version qg speaks", got)
		}
	})
}

func TestAcceptanceMCPRefusesUnknownOlderOrMalformedServerVersion(t *testing.T) {
	for _, answered := range []string{"2024-10-07", "1.0", "latest"} {
		t.Run(answered, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
				if message.Method == "initialize" {
					writeHTTPRPCResponse(t, w, message.ID, map[string]any{"protocolVersion": answered, "capabilities": map[string]any{}})
					return true
				}
				return false
			})
			_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"odd":{"type":"http","url":"`+server.URL+`"}}`))
			reason, skipped := acceptanceSkippedReason(runtime, "odd")
			if !skipped {
				t.Fatalf("server answering protocolVersion %q was connected; qg must refuse a version it does not know", answered)
			}
			if !strings.Contains(reason, answered) {
				t.Fatalf("visible reason %q does not name the protocol version %q", reason, answered)
			}
		})
	}
}
