package qgaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageSendsWindowAndScope(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			t.Fatalf("path = %q, want /usage", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"from":"2026-10-01T00:00:00Z","to":"2026-10-03T00:00:00Z","scope":"key","requests":2,"input_tokens":10,"output_tokens":4,"cached_input_tokens":3}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	report, err := client.Usage(context.Background(), UsageParams{
		From:  time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
		Scope: "key",
	})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	for _, want := range []string{"from=2026-10-01T00%3A00%3A00Z", "to=2026-10-03T00%3A00%3A00Z", "scope=key"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
	if report.Requests != 2 || report.InputTokens != 10 || report.OutputTokens != 4 || report.CachedInputTokens != 3 {
		t.Fatalf("report mismatch: %+v", report)
	}
	if report.Total() != 14 {
		t.Fatalf("Total() = %d, want input+output = 14", report.Total())
	}
}

func TestUsageOmitsParamsWhenUnset(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"from":"2026-10-08T16:17:51Z","to":"2026-10-09T16:17:51Z","scope":"user","requests":0,"input_tokens":0,"output_tokens":0,"cached_input_tokens":0}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	if _, err := client.Usage(context.Background(), UsageParams{}); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want empty (server defaults to 24h/user)", gotQuery)
	}
}

func TestUsageRejectsWindowOver31Days(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server must not be called for an invalid window")
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	_, err := client.Usage(context.Background(), UsageParams{
		From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("Usage accepted a >31d window")
	}
}

func TestUsageRejectsInvalidScopeAndInvertedWindow(t *testing.T) {
	client := NewClient(testKey)
	client.BaseURL = "http://invalid.example"
	if _, err := client.Usage(context.Background(), UsageParams{Scope: "org"}); err == nil {
		t.Fatal("Usage accepted scope=org")
	}
	if _, err := client.Usage(context.Background(), UsageParams{
		From: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}); err == nil {
		t.Fatal("Usage accepted from > to")
	}
}

func TestUsageCountsInputTokensAsIncludingCached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirrors the live gateway: input_tokens already contains the cached
		// slice; the client must not add it a second time.
		_, _ = w.Write([]byte(`{"from":"a","to":"b","scope":"user","requests":1,"input_tokens":100,"output_tokens":20,"cached_input_tokens":80}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	report, err := client.Usage(context.Background(), UsageParams{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if report.Total() != 120 {
		t.Fatalf("Total() = %d, want 120 (input already includes cached)", report.Total())
	}
}
