//go:build !plan9

package mcp

// Unit tests for the errno half of the transport classifiers. Each case
// carries the errno behind a text WITHOUT the English token, simulating a
// localized Windows message (the OS wording there cannot be trusted, so no
// substring may decide). They fail before the errno classification lands and
// pass after, on every platform.

import (
	"context"
	"net"
	"net/url"
	"syscall"
	"testing"
)

// localizedErrnoError carries a syscall errno behind a non-English message,
// the shape a Windows transport failure has after localization.
type localizedErrnoError struct {
	text  string
	errno error
}

func (err *localizedErrnoError) Error() string { return err.text }
func (err *localizedErrnoError) Unwrap() error { return err.errno }

// refusedDialShapes returns the error in the two shapes the classifiers see:
// the *url.Error the HTTP client produces (wrapping the dial OpError) and
// the bare error from other call sites.
func refusedDialShapes(errno error, text string) []error {
	inner := &localizedErrnoError{text: text, errno: errno}
	dial := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}, Err: inner}
	return []error{
		&url.Error{Op: "Post", URL: "http://127.0.0.1:9/mcp", Err: dial},
		dial,
	}
}

func TestTransientConnectErrorRecognizesErrnosWithoutEnglishText(t *testing.T) {
	ctx := context.Background()
	dnsErr := &net.DNSError{Err: "no such host", Name: "mcp.example.invalid", IsNotFound: true}
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused errno over url", refusedDialShapes(syscall.ECONNREFUSED, "nenhuma conexão pôde ser feita")[0], true},
		{"refused errno bare", refusedDialShapes(syscall.ECONNREFUSED, "nenhuma conexão pôde ser feita")[1], true},
		{"reset errno", &url.Error{Op: "Post", URL: "http://127.0.0.1:9/mcp", Err: &localizedErrnoError{text: "conexão reiniciada pelo par", errno: syscall.ECONNRESET}}, true},
		{"aborted errno", &url.Error{Op: "Post", URL: "http://127.0.0.1:9/mcp", Err: &localizedErrnoError{text: "conexão abortada", errno: syscall.ECONNABORTED}}, true},
		{"broken pipe errno", &url.Error{Op: "Post", URL: "http://127.0.0.1:9/mcp", Err: &localizedErrnoError{text: "canal quebrado", errno: syscall.EPIPE}}, true},
		{"timeout stays false", &ToolTimeoutError{Server: "s", Tool: "lookup"}, false},
		{"jsonrpc error stays false", &MCPRequestError{Method: "tools/call", Code: -32603, Message: "boom"}, false},
		{"http 404 stays false", &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 404}, false},
		{"dns stays false", dnsErr, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isTransientConnectError(ctx, testCase.err); got != testCase.want {
				t.Errorf("isTransientConnectError(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestDialPhaseErrorRecognizesRefusedErrno(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused errno over url", refusedDialShapes(syscall.ECONNREFUSED, "nenhuma conexão pôde ser feita")[0], true},
		{"refused errno bare", refusedDialShapes(syscall.ECONNREFUSED, "nenhuma conexão pôde ser feita")[1], true},
		// A reset means the server answered and dropped the connection: the
		// request may already be in its hands, so it is not dial-phase.
		{"reset errno stays false", &localizedErrnoError{text: "conexão reiniciada pelo par", errno: syscall.ECONNRESET}, false},
		{"dns stays true", &net.DNSError{Err: "no such host", Name: "mcp.example.invalid", IsNotFound: true}, true},
		{"http 404 stays false", &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 404}, false},
		{"jsonrpc error stays false", &MCPRequestError{Method: "tools/call", Code: -32603, Message: "boom"}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isDialPhaseError(testCase.err); got != testCase.want {
				t.Errorf("isDialPhaseError(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestDeadTransportErrorRecognizesErrnosWithoutEnglishText(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused errno", refusedDialShapes(syscall.ECONNREFUSED, "nenhuma conexão pôde ser feita")[0], true},
		{"reset errno", &localizedErrnoError{text: "conexão reiniciada pelo par", errno: syscall.ECONNRESET}, true},
		{"aborted errno", &localizedErrnoError{text: "conexão abortada", errno: syscall.ECONNABORTED}, true},
		{"broken pipe errno", &localizedErrnoError{text: "canal quebrado", errno: syscall.EPIPE}, true},
		{"timeout stays false", &ToolTimeoutError{Server: "s", Tool: "lookup"}, false},
		{"jsonrpc error stays false", &MCPRequestError{Method: "tools/call", Code: -32603, Message: "boom"}, false},
		{"http 404 stays false", &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 404}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isDeadTransportError(testCase.err); got != testCase.want {
				t.Errorf("isDeadTransportError(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}
