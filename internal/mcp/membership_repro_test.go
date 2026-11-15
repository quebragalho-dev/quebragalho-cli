package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// Commit A (M2): characterizes what a reconnect of a disabled server does
// today and pins the desired refusal. FAILS on untouched dev (the error is
// "unknown MCP server" instead of the actionable disabled hint); passes once
// the membership machine lands. The no-dial / no-tools / disabled-status
// assertions already hold on dev and document that the fix removes no
// capability: there is no live process to preserve.
func TestDisabledReconnectRefusedWithoutDial(t *testing.T) {
	acceptanceHermeticEnv(t)
	healthy := newAcceptanceHTTPServer(t, nil)
	dials := 0
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, acceptanceConfig(t,
		`{"off":{"type":"http","url":"`+healthy.URL+`","disabled":true}}`),
		RegisterOptions{ClientFactory: func(ctx context.Context, server Server) (ToolClient, error) {
			dials++
			return nil, errors.New("must not dial a disabled server")
		}})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if dials != 0 {
		t.Fatalf("startup dialed a disabled server %d times", dials)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = runtime.Reconnect(ctx, "off")
	if err == nil {
		t.Fatal("Reconnect(disabled) = nil, want refusal without dialing")
	}
	var unknown *UnknownServerError
	if errors.As(err, &unknown) {
		t.Fatalf("Reconnect(disabled) error = %v, want the actionable disabled hint, not unknown", err)
	}
	if !strings.Contains(err.Error(), "desabilitado") || !strings.Contains(err.Error(), "qg mcp enable off") {
		t.Fatalf("Reconnect(disabled) error = %v, want the actionable disabled hint", err)
	}
	if dials != 0 {
		t.Fatalf("Reconnect(disabled) dialed %d times, want zero", dials)
	}
	got := acceptanceStatus(t, runtime, "off")
	if got.State != ServerStateDisabled || got.ToolCount != 0 {
		t.Fatalf("off after refused reconnect = %#v, want disabled with no tools", got)
	}
	if _, ok := registry.Get("mcp_off_lookup"); ok {
		t.Fatal("disabled server advertised tools after refused reconnect")
	}
}
