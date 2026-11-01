package providerio

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func applyConversationHeader(t *testing.T, name, sessionID string) http.Header {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://api.quebragalho.dev/v1/chat/completions", nil)
	NewSessionHeader(name).Apply(request, sessionID)
	return request.Header
}

const conversationHeaderName = "X-QG-Conversation-ID"

// A valid session id reaches the gateway verbatim.
func TestSessionHeaderSendsValidConversationID(t *testing.T) {
	got := applyConversationHeader(t, conversationHeaderName, "qg_20260101120000_1234567890_1").Get(conversationHeaderName)
	if got != "qg_20260101120000_1234567890_1" {
		t.Fatalf("header = %q, want the session id verbatim", got)
	}
}

// No session means no header: the CLI must not invent a value.
func TestSessionHeaderOmitsEmptySessionID(t *testing.T) {
	header := NewSessionHeader(conversationHeaderName)
	for _, sessionID := range []string{"", "   "} {
		request := httptest.NewRequest(http.MethodPost, "https://api.quebragalho.dev/v1/chat/completions", nil)
		header.Apply(request, sessionID)
		if got := request.Header.Get(conversationHeaderName); got != "" {
			t.Fatalf("session id %q produced header %q, want no header", sessionID, got)
		}
	}
}

// The contract rejects over-long, padded, and control-byte values silently:
// none of them may reach the wire.
func TestSessionHeaderOmitsInvalidConversationID(t *testing.T) {
	header := NewSessionHeader(conversationHeaderName)
	invalid := []string{
		strings.Repeat("x", 129),
		" padded",
		"padded ",
		"with\ttab",
		"new\nline",
		"nul\x00byte",
		"carriage\rreturn",
	}
	for _, sessionID := range invalid {
		request := httptest.NewRequest(http.MethodPost, "https://api.quebragalho.dev/v1/chat/completions", nil)
		header.Apply(request, sessionID)
		if got := request.Header.Get(conversationHeaderName); got != "" {
			t.Fatalf("session id %q produced header %q, want no header", sessionID, got)
		}
	}
}

// A 128-byte printable value without padding or controls is the contract
// ceiling, so it still goes out.
func TestSessionHeaderSendsMaxLengthConversationID(t *testing.T) {
	sessionID := strings.Repeat("a", 128)
	got := applyConversationHeader(t, conversationHeaderName, sessionID).Get(conversationHeaderName)
	if got != sessionID {
		t.Fatalf("128-byte header = %q, want it sent whole", got)
	}
}

// An unnamed header is a no-op for every session id.
func TestSessionHeaderUnnamedSendsNothing(t *testing.T) {
	header := NewSessionHeader("")
	request := httptest.NewRequest(http.MethodPost, "https://api.quebragalho.dev/v1/chat/completions", nil)
	header.Apply(request, "qg_20260101120000_1234567890_1")
	if got := request.Header.Get(conversationHeaderName); got != "" {
		t.Fatalf("unnamed header produced %q, want no header", got)
	}
}
