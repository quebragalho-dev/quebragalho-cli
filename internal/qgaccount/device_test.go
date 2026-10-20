package qgaccount

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
)

type tokenStep struct {
	status     int
	body       string
	retryAfter string
}

func testTokenServer(t *testing.T, steps []tokenStep) (*httptest.Server, *int32) {
	t.Helper()
	var polls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		step := steps[int(atomic.AddInt32(&polls, 1))-1]
		if step.retryAfter != "" {
			w.Header().Set("Retry-After", step.retryAfter)
		}
		w.WriteHeader(step.status)
		_, _ = w.Write([]byte(step.body))
	}))
	t.Cleanup(server.Close)
	return server, &polls
}

func fastPollOptions() DevicePollOptions {
	return DevicePollOptions{
		Now:  time.Now,
		Wait: func(context.Context, time.Duration) error { return nil },
	}
}

func freshAuth() DeviceAuth {
	return DeviceAuth{
		DeviceCode: "dev-secret-code",
		Interval:   0, // rely on the client's 60/min floor
		ExpiresAt:  time.Now().Add(10 * time.Minute),
	}
}

func TestRequestDeviceCodeParsesRFCShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/code" {
			t.Fatalf("path = %q, want /auth/device/code", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("client_id"); got != DeviceClientID {
			t.Fatalf("client_id = %q, want %q", got, DeviceClientID)
		}
		_, _ = w.Write([]byte(`{"device_code":"dev-secret-code","user_code":"ABCD-1234","verification_uri":"https://app.quebragalho.dev/connect/cli","verification_uri_complete":"https://app.quebragalho.dev/connect/cli?user_code=ABCD-1234","expires_in":600,"interval":1}`))
	}))
	defer server.Close()
	client := NewClient("")
	client.AuthBaseURL = server.URL

	auth, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if auth.DeviceCode != "dev-secret-code" || auth.UserCode != "ABCD-1234" {
		t.Fatalf("codes mismatch: %+v", auth)
	}
	if auth.VerificationURI != "https://app.quebragalho.dev/connect/cli" {
		t.Fatalf("verification_uri = %q", auth.VerificationURI)
	}
	if !strings.Contains(auth.VerificationURIComplete, "user_code=ABCD-1234") {
		t.Fatalf("verification_uri_complete = %q", auth.VerificationURIComplete)
	}
	if auth.Interval != time.Second {
		t.Fatalf("Interval = %s, want 1s from the response", auth.Interval)
	}
	if auth.ExpiresAt.IsZero() || time.Until(auth.ExpiresAt) <= 0 {
		t.Fatalf("ExpiresAt = %v, want in the future", auth.ExpiresAt)
	}
}

func TestRequestDeviceCodeDefaultsWhenFieldsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"d","user_code":"U","verification_uri":"https://x"}`))
	}))
	defer server.Close()
	client := NewClient("")
	client.AuthBaseURL = server.URL

	auth, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if auth.Interval != 5*time.Second {
		t.Fatalf("Interval = %s, want the RFC default 5s", auth.Interval)
	}
	if auth.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt unset: a missing expires_in must still bound the flow")
	}
}

func TestPollDeviceTokenPendingThenSuccess(t *testing.T) {
	server, polls := testTokenServer(t, []tokenStep{
		{status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`},
		{status: http.StatusOK, body: `{"api_key":"qg-sk-from-device-0123456789"}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL

	key, err := client.PollDeviceToken(context.Background(), freshAuth(), fastPollOptions())
	if err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if key != "qg-sk-from-device-0123456789" {
		t.Fatalf("key = %q", key)
	}
	if atomic.LoadInt32(polls) != 2 {
		t.Fatalf("polls = %d, want 2", *polls)
	}
}

func TestPollDeviceTokenHonors429RetryAfter(t *testing.T) {
	server, polls := testTokenServer(t, []tokenStep{
		{status: http.StatusTooManyRequests, body: `{"error":"rate_limited","error_description":"Too many login attempts."}`, retryAfter: "2"},
		{status: http.StatusOK, body: `{"api_key":"qg-sk-after-429-0123456789"}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL

	var waited time.Duration
	opts := fastPollOptions()
	opts.Wait = func(_ context.Context, d time.Duration) error {
		waited += d
		return nil
	}
	key, err := client.PollDeviceToken(context.Background(), freshAuth(), opts)
	if err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if key == "" {
		t.Fatal("empty key after 429 recovery")
	}
	if waited < 2*time.Second {
		t.Fatalf("waited %s, want at least the Retry-After 2s", waited)
	}
	if atomic.LoadInt32(polls) != 2 {
		t.Fatalf("polls = %d, want the 429 to consume one poll", *polls)
	}
}

func TestPollDeviceTokenSlowDownGrowsInterval(t *testing.T) {
	server, _ := testTokenServer(t, []tokenStep{
		{status: http.StatusBadRequest, body: `{"error":"slow_down"}`},
		{status: http.StatusOK, body: `{"api_key":"qg-sk-slow-0123456789abcdef"}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL

	var waits []time.Duration
	opts := fastPollOptions()
	opts.Wait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	if _, err := client.PollDeviceToken(context.Background(), freshAuth(), opts); err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want two", waits)
	}
	if waits[1] <= waits[0] {
		t.Fatalf("waits = %v, want the interval to grow after slow_down", waits)
	}
}

func TestPollDeviceTokenExpiredAndDenied(t *testing.T) {
	for _, tc := range []struct {
		body string
		want error
	}{
		{`{"error":"expired_token"}`, ErrDeviceCodeExpired},
		{`{"error":"access_denied"}`, ErrAccessDenied},
	} {
		server, _ := testTokenServer(t, []tokenStep{{status: http.StatusBadRequest, body: tc.body}})
		client := NewClient("")
		client.AuthBaseURL = server.URL
		_, err := client.PollDeviceToken(context.Background(), freshAuth(), fastPollOptions())
		if !errors.Is(err, tc.want) {
			t.Fatalf("err = %v, want %v", err, tc.want)
		}
		server.Close()
	}
}

func TestPollDeviceTokenErrorsNeverLeakSecrets(t *testing.T) {
	server, _ := testTokenServer(t, []tokenStep{
		{status: http.StatusUnauthorized, body: `{"error":{"message":"device dev-secret-code rejected"}}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL
	_, err := client.PollDeviceToken(context.Background(), freshAuth(), fastPollOptions())
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "dev-secret-code") {
		t.Fatalf("device_code leaked into error: %q", err.Error())
	}
}

func TestPollDeviceTokenEnforcesOneSecondFloor(t *testing.T) {
	server, _ := testTokenServer(t, []tokenStep{
		{status: http.StatusOK, body: `{"api_key":"qg-sk-floor-0123456789abcdef"}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL
	opts := fastPollOptions()
	var waits []time.Duration
	opts.Wait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	if _, err := client.PollDeviceToken(context.Background(), freshAuth(), opts); err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	// One wait before the first poll: at least the 1s floor (60/min cap)
	// even when the server asked for less.
	if len(waits) == 0 || waits[0] < time.Second {
		t.Fatalf("waits = %v, want a >=1s floor before polling", waits)
	}
}

func TestPollDeviceTokenStopsOnExpiredDeadline(t *testing.T) {
	client := NewClient("")
	auth := freshAuth()
	auth.ExpiresAt = time.Now().Add(-time.Minute)
	_, err := client.PollDeviceToken(context.Background(), auth, fastPollOptions())
	if !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("err = %v, want ErrDeviceCodeExpired", err)
	}
}

func TestPollDeviceTokenAcceptsKeyAndAccessTokenAliases(t *testing.T) {
	for _, field := range []string{"api_key", "key", "access_token"} {
		server, _ := testTokenServer(t, []tokenStep{
			{status: http.StatusOK, body: `{"` + field + `":"qg-sk-alias-0123456789abcdef"}`},
		})
		client := NewClient("")
		client.AuthBaseURL = server.URL
		key, err := client.PollDeviceToken(context.Background(), freshAuth(), fastPollOptions())
		if err != nil {
			t.Fatalf("field %s: %v", field, err)
		}
		if key != "qg-sk-alias-0123456789abcdef" {
			t.Fatalf("field %s: key = %q", field, key)
		}
		server.Close()
	}
}

func TestPollDeviceTokenClampsRetryAfterToExpiry(t *testing.T) {
	// First poll is rate-limited with a huge Retry-After; the code expires in
	// ~2s. The wait must be clamped to the remaining lifetime, not 1 hour.
	server, polls := testTokenServer(t, []tokenStep{
		{status: http.StatusTooManyRequests, body: `{"error":"rate_limited"}`, retryAfter: "3600"},
		{status: http.StatusOK, body: `{"api_key":"qg-sk-clamp-0123456789abcdef"}`},
	})
	client := NewClient("")
	client.AuthBaseURL = server.URL

	auth := freshAuth()
	auth.ExpiresAt = time.Now().Add(2 * time.Second)
	var waits []time.Duration
	opts := fastPollOptions()
	opts.Wait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	// Either it succeeds within the clamped window or it expires; either way no
	// wait may approach the un-clamped 3600s.
	_, _ = client.PollDeviceToken(context.Background(), auth, opts)
	for i, w := range waits {
		if w > 3*time.Second {
			t.Fatalf("wait %d = %s, want clamped to the ~2s remaining lifetime (not 3600s)", i, w)
		}
	}
	if len(waits) == 0 {
		t.Fatal("expected at least one wait")
	}
	_ = polls
}

func TestDeviceFlowPrimitivesAreSharedWithOAuth(t *testing.T) {
	// The QG device flow and the generic OAuth device flow must not drift: they
	// are the same RFC 8628 types, sentinels and default lifetime. DeviceAuth is
	// an alias, so no type has to be declared here — the helper call below only
	// builds while both arguments resolve to one single type.
	assertSameDeviceAuthType(DeviceAuth{}, oauth.DeviceAuth{})
	if !errors.Is(ErrAuthorizationPending, oauth.ErrAuthorizationPending) {
		t.Fatal("ErrAuthorizationPending is not oauth.ErrAuthorizationPending")
	}
	if !errors.Is(ErrSlowDown, oauth.ErrSlowDown) {
		t.Fatal("ErrSlowDown is not oauth.ErrSlowDown")
	}
	if defaultDeviceCodeLifetime != oauth.DefaultDeviceCodeLifetime {
		t.Fatalf("lifetime = %s, want oauth's %s", defaultDeviceCodeLifetime, oauth.DefaultDeviceCodeLifetime)
	}
	if !errors.Is(errUnsafeRedirect, oauth.ErrUnsafeRedirect) {
		t.Fatal("errUnsafeRedirect is not oauth.ErrUnsafeRedirect")
	}
}

// assertSameDeviceAuthType accepts values of one single type: a structurally
// identical but distinct DeviceAuth would fail to compile here.
func assertSameDeviceAuthType(_, _ DeviceAuth) {}

// The gateway's token endpoint requires the RFC 8628 grant_type field: a poll
// without it fails with HTTP 400 "invalid_request: grant_type is required"
// (reproduced against the live server with the worktree binary). The client
// must send grant_type=urn:ietf:params:oauth:grant-type:device_code, the same
// value the sibling oauth package already sends (oauth/device.go).
func TestPollDeviceTokenSendsGrantType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"grant_type is required."}`))
			return
		}
		_, _ = w.Write([]byte(`{"api_key":"qg-sk-from-device-0123456789"}`))
	}))
	defer server.Close()
	client := NewClient("")
	client.AuthBaseURL = server.URL

	key, err := client.PollDeviceToken(context.Background(), freshAuth(), fastPollOptions())
	if err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if key != "qg-sk-from-device-0123456789" {
		t.Fatalf("key = %q", key)
	}
}
