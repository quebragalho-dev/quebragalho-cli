package providerio

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallerOwnsRetryDefaultsFalse(t *testing.T) {
	if CallerOwnsRetry(context.Background()) {
		t.Fatal("a bare context must not carry the caller-retry marker")
	}
	if !CallerOwnsRetry(WithCallerRetry(context.Background())) {
		t.Fatal("WithCallerRetry must mark the context")
	}
}

// With the caller-retry marker the agent loop owns the policy: one attempt,
// no status retries, no pre-send retries, no waits — so retries never stack
// across layers.
func TestSendWithRetryCallerOwnsRetryMakesOneAttempt(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	start := time.Now()
	resp, err := SendWithRetry(WithCallerRetry(context.Background()), server.Client(),
		http.MethodPost, server.URL, []byte("{}"), nil, 6)
	if err != nil {
		t.Fatalf("single attempt must return the response, got error %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("caller-owned retry must issue exactly 1 request, got %d", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("single attempt must not wait, took %v", elapsed)
	}
}

// A provably pre-send failure (refused dial) is also a single attempt under
// the marker — the agent schedules the wait, not this layer.
func TestSendWithRetryCallerOwnsRetrySkipsPreSendRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	var dials int32
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		atomic.AddInt32(&dials, 1)
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, addr)
	}
	client := &http.Client{Transport: transport}

	_, err := SendWithRetry(WithCallerRetry(context.Background()), client,
		http.MethodPost, url, []byte("{}"), nil, 6)
	if err == nil {
		t.Fatal("want the dial error, got nil")
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("caller-owned retry must dial exactly once, got %d", got)
	}
}
