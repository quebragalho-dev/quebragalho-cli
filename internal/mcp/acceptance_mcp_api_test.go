package mcp

// MCP acceptance tests for runtime status, reconnect, the catalog refresh
// between turns and project approvals.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptanceStatus(t *testing.T, runtime *Runtime, server string) ServerStatus {
	t.Helper()
	for _, status := range runtime.Status() {
		if status.Name == server {
			return status
		}
	}
	t.Fatalf("Status() has no entry for %q: %#v", server, runtime.Status())
	return ServerStatus{}
}

// Every configured server has a state, a failure always has a reason, and
// disabled servers stay listed so /mcp can turn them back on.
func TestAcceptanceMCPStatusReportsStateAndReason(t *testing.T) {
	acceptanceHermeticEnv(t)
	healthy := newAcceptanceHTTPServer(t, nil)
	broken := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{`+
		`"ok":{"type":"http","url":"`+healthy.URL+`"},`+
		`"broken":{"type":"http","url":"`+broken.URL+`"},`+
		`"login":{"type":"http","url":"`+healthy.URL+`/oauth","auth":"oauth"},`+
		`"off":{"type":"http","url":"`+healthy.URL+`","disabled":true}}`))

	if got := acceptanceStatus(t, runtime, "ok"); got.State != ServerStateConnected || got.ToolCount != 1 {
		t.Fatalf("ok = %#v, want connected with 1 tool", got)
	}
	if got := acceptanceStatus(t, runtime, "broken"); got.State != ServerStateFailed || !strings.Contains(got.Reason, "503") {
		t.Fatalf("broken = %#v, want failed with the HTTP 503 in the reason", got)
	}
	if got := acceptanceStatus(t, runtime, "login"); got.State != ServerStateNeedsAuth {
		t.Fatalf("login = %#v, want needs_auth for an OAuth server with no token", got)
	}
	if got := acceptanceStatus(t, runtime, "off"); got.State != ServerStateDisabled {
		t.Fatalf("off = %#v, want disabled and still listed", got)
	}
	for _, status := range runtime.Status() {
		if status.State == ServerStateFailed && strings.TrimSpace(status.Reason) == "" {
			t.Fatalf("failed server %q has no reason", status.Name)
		}
	}
}

// The /mcp "reconectar" action: a server that failed at startup comes back
// without restarting qg.
func TestAcceptanceMCPReconnectActionRecoversFailedServer(t *testing.T) {
	acceptanceHermeticEnv(t)
	var healthy atomic.Bool
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if healthy.Load() {
			return false
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	})
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"late":{"type":"http","url":"`+server.URL+`"}}`))
	if got := acceptanceStatus(t, runtime, "late"); got.State != ServerStateFailed {
		t.Fatalf("late = %#v, want failed at startup", got)
	}
	healthy.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeoutBound)
	defer cancel()
	if err := runtime.Reconnect(ctx, "late"); err != nil {
		t.Fatalf("Reconnect() error = %v", err)
	}
	if got := acceptanceStatus(t, runtime, "late"); got.State != ServerStateConnected {
		t.Fatalf("late after reconnect = %#v, want connected", got)
	}
	if result, ok := acceptanceRun(t, registry, "mcp_late_lookup", acceptanceTimeoutBound); !ok || result.Status != tools.StatusOK {
		t.Fatalf("tool after reconnect = %#v", result)
	}
}

// list_changed is applied by the turn driver between turns, never in the
// middle of one.
func TestAcceptanceMCPListChangedAppliesOnNextTurn(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "list-changed")
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"live":`+helper.configJSON("")+`}`))
	if result, ok := acceptanceRun(t, registry, "mcp_live_lookup", 10*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("call that triggers list_changed failed: %#v", result)
	}
	time.Sleep(300 * time.Millisecond)
	if registryHasTool(registry, "mcp_live_lookup_v2") {
		t.Fatal("catalog changed before the turn boundary")
	}
	changed, err := runtime.RefreshPendingTools(context.Background())
	if err != nil {
		t.Fatalf("RefreshPendingTools() error = %v", err)
	}
	if !changed {
		t.Fatal("RefreshPendingTools() = false after notifications/tools/list_changed")
	}
	if !registryHasTool(registry, "mcp_live_lookup_v2") || registryHasTool(registry, "mcp_live_lookup") {
		t.Fatal("next turn did not get the re-listed catalog")
	}
	again, err := runtime.RefreshPendingTools(context.Background())
	if err != nil || again {
		t.Fatalf("second RefreshPendingTools() = %v, %v; want no change without a new notification", again, err)
	}
}

// Approval is remembered per project and per server identity. If the project
// later changes what the server runs, the old approval no longer applies.
func TestAcceptanceMCPProjectApprovalRememberedPerProjectAndIdentity(t *testing.T) {
	root := acceptanceHermeticEnv(t)
	approval := ProjectApproval{ProjectRoot: filepath.Join(root, "repo"), ServerName: "proj", Identity: "identity-1"}

	store, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals() error = %v", err)
	}
	if store.IsApproved(approval) {
		t.Fatal("project server must start unapproved")
	}
	if err := store.Approve(approval); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	reopened, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if !reopened.IsApproved(approval) {
		t.Fatal("approval was not persisted")
	}
	otherProject := approval
	otherProject.ProjectRoot = filepath.Join(root, "other")
	if reopened.IsApproved(otherProject) {
		t.Fatal("approval leaked to another project")
	}
	changedCommand := approval
	changedCommand.Identity = "identity-2"
	if reopened.IsApproved(changedCommand) {
		t.Fatal("approval survived a change of the server's command/url (identity)")
	}
}

// An expanded ${VAR} value never appears in a failure reason. Skipped feeds
// the startup stderr warning and `qg mcp check`; Status feeds /mcp and
// `qg mcp list`. net/http dial errors echo the full URL.
func TestAcceptanceMCPFailureReasonNeverCarriesExpandedSecret(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_SECRET", "ambient-secret-value")
	dead := newAcceptanceHTTPServer(t, nil)
	deadURL := dead.URL
	dead.Close()
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"secretive":{"type":"http",`+
		`"url":"`+deadURL+`/mcp?k=${QG_ACCEPT_SECRET}","headers":{"X-K":"${QG_ACCEPT_SECRET}"}}}`))

	reason, skipped := acceptanceSkippedReason(runtime, "secretive")
	if !skipped {
		t.Fatal("server on a closed port was not reported as failed")
	}
	if strings.Contains(reason, "ambient-secret-value") {
		t.Fatalf("skipped reason carries the expanded secret: %q", reason)
	}
	status := acceptanceStatus(t, runtime, "secretive")
	if status.State != ServerStateFailed || strings.TrimSpace(status.Reason) == "" {
		t.Fatalf("secretive = %#v, want failed with a reason", status)
	}
	if strings.Contains(status.Reason, "ambient-secret-value") {
		t.Fatalf("status reason carries the expanded secret: %q", status.Reason)
	}
}
