package qgaccount

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "qg-sk-test-0123456789abcdef"

func TestClientSendsAuthInHeadersOnly(t *testing.T) {
	var gotAuth, gotXAPI, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXAPI = r.Header.Get("x-api-key")
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]string{"currency": "BRL", "available": "1.00", "default": "1.00", "bonus": "0"})
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	if _, err := client.Balance(context.Background()); err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if gotAuth != "Bearer "+testKey {
		t.Fatalf("Authorization = %q, want Bearer <key>", gotAuth)
	}
	if gotXAPI != testKey {
		t.Fatalf("x-api-key = %q, want the raw key", gotXAPI)
	}
	if strings.Contains(gotQuery, testKey) || strings.Contains(server.URL+"/"+gotQuery, testKey) {
		t.Fatalf("API key leaked into the URL query: %q", gotQuery)
	}
}

func TestAPIErrorRedactsKeyFromServerMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key ` + testKey + `","type":"authentication_error","code":"invalid_api_key"}}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	_, err := client.Balance(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
	if strings.Contains(apiErr.Error(), testKey) {
		t.Fatalf("API key leaked into the error: %q", apiErr.Error())
	}
	if !strings.Contains(apiErr.Message, "invalid api key") {
		t.Fatalf("Message = %q, want the server's text minus the key", apiErr.Message)
	}
}

func TestParseErrorMessageShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"nested message", `{"error":{"message":"invalid api key","type":"t"}}`, "invalid api key"},
		{"code + description", `{"error":"rate_limited","error_description":"Too many login attempts."}`, "rate_limited: Too many login attempts."},
		{"bare code", `{"error":"authorization_pending"}`, "authorization_pending"},
		{"plain text", `try later`, "try later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseErrorMessage([]byte(tt.body)); got != tt.want {
				t.Fatalf("parseErrorMessage(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestClientRefusesCrossHostRedirectSoXAPIKeyNeverLeaks(t *testing.T) {
	var targetGotKey atomic.Bool
	var targetGotAny atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetGotAny.Store(true)
		if r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "" {
			targetGotKey.Store(true)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 307 replays method + body + headers; a naive client would copy
		// x-api-key to the target host (Go only strips Authorization).
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewClient(testKey)
	client.BaseURL = source.URL
	_, err := client.Balance(context.Background())
	if err == nil {
		t.Fatal("expected the redirect to be refused, got success")
	}
	// Give any (wrongly) followed request a moment to land.
	time.Sleep(50 * time.Millisecond)
	if targetGotAny.Load() {
		t.Fatal("client followed a cross-host redirect; credential endpoints must refuse redirects")
	}
	if targetGotKey.Load() {
		t.Fatal("x-api-key leaked to the redirect target")
	}
}
