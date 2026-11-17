//go:build windows

package mcp

// Windows proof for the errno half of the transport classifiers: a real dial
// against a closed loopback port carries the WSA errno behind the OS wording,
// and every classifier must recognize it. Runs only in the Windows job.

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// closedLoopbackPort reserves a loopback port and closes it, so dials to it
// are refused by the OS with the platform wording.
func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func TestWindowsRefusedLoopbackClassifiesWithoutEnglishToken(t *testing.T) {
	addr := closedLoopbackPort(t)

	_, dialErr := net.DialTimeout("tcp", addr, 10*time.Second)
	if dialErr == nil {
		t.Fatalf("dial to closed %s succeeded, want refused", addr)
	}
	if strings.Contains(strings.ToLower(dialErr.Error()), "connection refused") {
		t.Fatalf("dial error already carries the English token, this test proves nothing: %v", dialErr)
	}
	if !isDialPhaseError(dialErr) {
		t.Errorf("isDialPhaseError(%v) = false, want true", dialErr)
	}
	if !isDeadTransportError(dialErr) {
		t.Errorf("isDeadTransportError(%v) = false, want true", dialErr)
	}
	if !isTransientConnectError(context.Background(), dialErr) {
		t.Errorf("isTransientConnectError(%v) = false, want true", dialErr)
	}

	// The HTTP shape: what http.Client hands the classifiers on a refused
	// dial, with the OS wording and no English token.
	_, httpErr := http.Post("http://"+addr+"/mcp", "application/json", nil)
	if httpErr == nil {
		t.Fatalf("post to closed %s succeeded, want refused", addr)
	}
	if strings.Contains(strings.ToLower(httpErr.Error()), "connection refused") {
		t.Fatalf("http error already carries the English token, this test proves nothing: %v", httpErr)
	}
	if !isDialPhaseError(httpErr) {
		t.Errorf("isDialPhaseError(%v) = false, want true", httpErr)
	}
	if !isTransientConnectError(context.Background(), httpErr) {
		t.Errorf("isTransientConnectError(%v) = false, want true", httpErr)
	}
	if !isDeadTransportError(httpErr) {
		t.Errorf("isDeadTransportError(%v) = false, want true", httpErr)
	}
}
