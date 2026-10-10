package qgaccount

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UsageMaxWindow is the widest from/to span the usage endpoint accepts.
const UsageMaxWindow = 31 * 24 * time.Hour

// UsageParams selects the reporting window and scope. Zero times omit the
// query parameter, letting the gateway default to the last 24 hours.
type UsageParams struct {
	From  time.Time
	To    time.Time
	Scope string // "", "user" or "key"
}

// UsageReport is the gateway's token accounting for the requested window.
type UsageReport struct {
	From              string `json:"from"`
	To                string `json:"to"`
	Scope             string `json:"scope"`
	Requests          int64  `json:"requests"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
}

// Total is billed tokens for the window. InputTokens already contains
// CachedInputTokens (gateway contract), so total is simply input + output.
func (r UsageReport) Total() int64 {
	return r.InputTokens + r.OutputTokens
}

// Usage fetches the token accounting window from GET /v1/usage.
func (c *Client) Usage(ctx context.Context, params UsageParams) (UsageReport, error) {
	if params.Scope != "" && params.Scope != "user" && params.Scope != "key" {
		return UsageReport{}, fmt.Errorf("qg account: scope must be user or key, got %q", params.Scope)
	}
	if !params.From.IsZero() && !params.To.IsZero() {
		if !params.From.Before(params.To) {
			return UsageReport{}, fmt.Errorf("qg account: usage window start must be before end")
		}
		if params.To.Sub(params.From) > UsageMaxWindow {
			return UsageReport{}, fmt.Errorf("qg account: usage window %s exceeds the 31-day maximum", params.To.Sub(params.From))
		}
	}
	query := url.Values{}
	if !params.From.IsZero() {
		query.Set("from", params.From.UTC().Format(time.RFC3339))
	}
	if !params.To.IsZero() {
		query.Set("to", params.To.UTC().Format(time.RFC3339))
	}
	if params.Scope != "" {
		query.Set("scope", params.Scope)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/usage"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var report UsageReport
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &report); err != nil {
		return UsageReport{}, err
	}
	return report, nil
}
