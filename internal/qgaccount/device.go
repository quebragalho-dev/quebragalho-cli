package qgaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// Terminal (and retryable) states of the QG device-flow poll. The two
// retryable RFC 8628 states are oauth's own sentinels, so both device flows
// answer errors.Is the same way; the rest are QG-specific.
var (
	ErrAuthorizationPending = oauth.ErrAuthorizationPending
	ErrSlowDown             = oauth.ErrSlowDown
	ErrDeviceCodeExpired    = errors.New("qg account: device code expired")
	ErrAccessDenied         = errors.New("qg account: device authorization denied")
	ErrRateLimited          = errors.New("qg account: device endpoint rate limited")
)

// defaultDevicePollFloor caps the poll rate at 60 reads/min even when the
// server advertises a shorter interval.
const defaultDevicePollFloor = time.Second

// defaultDeviceCodeLifetime bounds a code response that omits expires_in. The
// bound and the type below are oauth's: one RFC 8628 implementation to share.
const defaultDeviceCodeLifetime = oauth.DefaultDeviceCodeLifetime

// DeviceAuth is the result of the device-code request: what to show the
// user, and what to poll with. It is oauth.DeviceAuth, not a copy of it.
type DeviceAuth = oauth.DeviceAuth

// DevicePollOptions makes the poll loop testable: Now replaces the clock and
// Wait replaces sleeping. Zero values select the production behavior.
type DevicePollOptions struct {
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	MinInterval time.Duration
}

// RequestDeviceCode starts the QG device flow: the app issues a short code
// the user approves at /connect/cli; no browser is required on this side.
func (c *Client) RequestDeviceCode(ctx context.Context) (DeviceAuth, error) {
	endpoint := strings.TrimRight(c.AuthBaseURL, "/") + "/auth/device/code"
	form := url.Values{}
	form.Set("client_id", DeviceClientID)
	var parsed struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("qg account: build device-code request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("qg account: device-code request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DeviceAuth{}, &APIError{
			StatusCode: response.StatusCode,
			Message:    c.redactText(parseErrorMessage(body)),
		}
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DeviceAuth{}, fmt.Errorf("qg account: decode device-code response: %w", err)
	}
	if strings.TrimSpace(parsed.DeviceCode) == "" || strings.TrimSpace(parsed.UserCode) == "" {
		return DeviceAuth{}, errors.New("qg account: device-code response missing device_code/user_code")
	}
	interval := time.Duration(parsed.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresIn := time.Duration(parsed.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = defaultDeviceCodeLifetime
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return DeviceAuth{
		DeviceCode:              parsed.DeviceCode,
		UserCode:                parsed.UserCode,
		VerificationURI:         parsed.VerificationURI,
		VerificationURIComplete: parsed.VerificationURIComplete,
		Interval:                interval,
		ExpiresAt:               now().Add(expiresIn),
	}, nil
}

// PollDeviceToken polls until the user approves the code in the browser and
// the app returns the account API key. 429 answers are waited out via
// Retry-After; the loop never exceeds 60 reads/min.
func (c *Client) PollDeviceToken(ctx context.Context, auth DeviceAuth, opts DevicePollOptions) (string, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	wait := opts.Wait
	if wait == nil {
		wait = waitWithContext
	}
	floor := opts.MinInterval
	if floor <= 0 {
		floor = defaultDevicePollFloor
	}
	interval := auth.Interval
	if interval < floor {
		interval = floor
	}
	for {
		if !auth.ExpiresAt.IsZero() && !auth.ExpiresAt.After(now()) {
			return "", ErrDeviceCodeExpired
		}
		if err := wait(ctx, interval); err != nil {
			return "", fmt.Errorf("qg account: device poll interrupted: %w", err)
		}
		if !auth.ExpiresAt.IsZero() && !auth.ExpiresAt.After(now()) {
			return "", ErrDeviceCodeExpired
		}
		key, retryAfter, err := c.pollDeviceOnce(ctx, auth)
		switch {
		case err == nil:
			return key, nil
		case errors.Is(err, ErrAuthorizationPending):
			// keep waiting at the current interval
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
			if interval < floor {
				interval = floor
			}
		case errors.Is(err, ErrRateLimited):
			backoff := retryAfter
			if backoff < interval {
				backoff = interval
			}
			// Never wait past the code's own expiry: a huge Retry-After would
			// sleep the full window and only then report the code as expired.
			if !auth.ExpiresAt.IsZero() {
				remaining := auth.ExpiresAt.Sub(now())
				if remaining <= 0 {
					return "", ErrDeviceCodeExpired
				}
				if backoff > remaining {
					backoff = remaining
				}
			}
			if err := wait(ctx, backoff); err != nil {
				return "", fmt.Errorf("qg account: device poll interrupted: %w", err)
			}
		default:
			return "", err
		}
	}
}

// pollDeviceOnce makes a single token poll. The device_code never appears in
// returned errors.
func (c *Client) pollDeviceOnce(ctx context.Context, auth DeviceAuth) (string, time.Duration, error) {
	endpoint := strings.TrimRight(c.AuthBaseURL, "/") + "/auth/device/token"
	form := url.Values{}
	// RFC 8628 §3.5: the token poll is an access-token request, so it carries
	// the device_code grant type. Without it the gateway answers HTTP 400
	// "invalid_request: grant_type is required".
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", auth.DeviceCode)
	form.Set("client_id", DeviceClientID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("qg account: build device-token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("qg account: device-token request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

	if response.StatusCode == http.StatusTooManyRequests {
		return "", parseRetryAfter(response.Header.Get("Retry-After")), ErrRateLimited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := parseErrorCode(body)
		switch code {
		case "authorization_pending":
			return "", 0, ErrAuthorizationPending
		case "slow_down":
			return "", 0, ErrSlowDown
		case "expired_token":
			return "", 0, ErrDeviceCodeExpired
		case "access_denied":
			return "", 0, ErrAccessDenied
		}
		options := redaction.Options{ExtraSecretValues: []string{auth.DeviceCode}}
		if c.APIKey != "" {
			options.ExtraSecretValues = append(options.ExtraSecretValues, c.APIKey)
		}
		return "", 0, &APIError{
			StatusCode: response.StatusCode,
			Message:    redaction.RedactString(parseErrorMessage(body), options),
		}
	}
	var parsed struct {
		APIKey      string `json:"api_key"`
		Key         string `json:"key"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("qg account: decode device-token response: %w", err)
	}
	for _, candidate := range []string{parsed.APIKey, parsed.Key, parsed.AccessToken} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate), 0, nil
		}
	}
	return "", 0, errors.New("qg account: device-token response missing the API key")
}

// parseErrorCode digs the error string out of the auth app's JSON shapes.
func parseErrorCode(body []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Error) == 0 {
		return ""
	}
	var code string
	if err := json.Unmarshal(envelope.Error, &code); err == nil {
		return code
	}
	return ""
}

// parseRetryAfter reads the Retry-After header in the only form the auth app
// emits (delta-seconds). Anything else falls back to zero.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// waitWithContext sleeps unless the context is canceled first.
func waitWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// errUnsafeRedirect is oauth's own refusal, reused so credential endpoints
// fail identically in both packages.
var errUnsafeRedirect = oauth.ErrUnsafeRedirect

// httpClient returns a shallow copy of the client whose redirect policy
// refuses to follow ANY redirect. A credential-bearing request (Bearer +
// x-api-key, or a device-flow POST body) must never let a 307/308 replay its
// headers/body to an unvalidated origin. The caller's client is unmodified.
func (c *Client) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	copied := *base
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errUnsafeRedirect
	}
	return &copied
}
