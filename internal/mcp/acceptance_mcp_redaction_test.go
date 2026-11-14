package mcp

// An expanded ${VAR} value never leaves the package: not in a failure reason,
// a status, or the output the model reads. Redaction by URL shape is not
// enough, because hosted endpoints often carry the key in the URL path and
// servers echo their argv on stderr.

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestAcceptanceMCPExpandedSecretInURLPathIsNeverShown(t *testing.T) {
	acceptanceHermeticEnv(t)
	const secret = "sk-path-secret-4242"
	t.Setenv("QG_ACCEPT_SECRET", secret)
	var dead atomic.Bool
	live := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, _ rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if dead.Load() {
			hijackClose(w)
			return true
		}
		return false
	})
	cfg := acceptanceConfig(t, `{"live":{"type":"http","url":"`+live.URL+`/${QG_ACCEPT_SECRET}/mcp"},`+
		`"down":{"type":"http","url":"http://127.0.0.1:1/${QG_ACCEPT_SECRET}/mcp"}}`)
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, cfg, RegisterOptions{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	leaks := []string{}
	reason, skipped := acceptanceSkippedReason(runtime, "down")
	if !skipped || !strings.Contains(reason, "127.0.0.1:1") {
		t.Fatalf("server on a closed port: skipped=%v reason=%q, want a reason naming the address", skipped, reason)
	}
	if strings.Contains(reason, secret) {
		leaks = append(leaks, "startup failure reason: "+reason)
	}
	for _, status := range runtime.Status() {
		if strings.Contains(status.Reason, secret) {
			leaks = append(leaks, "status of "+status.Name+": "+status.Reason)
		}
	}
	dead.Store(true)
	result, returned := acceptanceRun(t, registry, "mcp_live_lookup", 90*time.Second)
	if !returned {
		t.Fatal("call to a server that drops every connection never returned")
	}
	if strings.Contains(result.Output, secret) {
		leaks = append(leaks, "model output: "+result.Output)
	}
	if len(leaks) > 0 {
		t.Fatalf("expanded URL-path secret shown in %d place(s):\n%s", len(leaks), strings.Join(leaks, "\n"))
	}
}

func TestAcceptanceMCPExpandedArgEchoedOnStderrIsNeverShown(t *testing.T) {
	acceptanceHermeticEnv(t)
	const secret = "sk-arg-secret-7777"
	t.Setenv("QG_ACCEPT_SECRET", secret)
	server := newLifecycleServer(t, "echo-args")
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t, "${QG_ACCEPT_SECRET}")+`}`))

	reason, skipped := acceptanceSkippedReason(runtime, "s")
	if !skipped || !strings.Contains(reason, "bad credentials") {
		t.Fatalf("server that exits at start: skipped=%v reason=%q, want a reason with its stderr diagnostic", skipped, reason)
	}
	status := acceptanceStatus(t, runtime, "s")
	if strings.Contains(reason, secret) || strings.Contains(status.Reason, secret) {
		t.Fatalf("expanded arg echoed by the server reached the failure reason:\nskipped: %s\nstatus:  %s", reason, status.Reason)
	}
}
