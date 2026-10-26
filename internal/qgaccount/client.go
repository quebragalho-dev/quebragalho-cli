package qgaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

const (
	// DefaultBaseURL is the QG gateway root the account endpoints live under.
	DefaultBaseURL = "https://api.quebragalho.dev/v1"
	// DefaultAuthBaseURL is the app origin that hosts the CLI device-flow endpoints.
	DefaultAuthBaseURL = "https://app.quebragalho.dev/api/cli/v1"
	// DeviceClientID is the registered OAuth client for the QG CLI device flow.
	DeviceClientID = "quebragalho-cli"
)

// Client talks to the QG account endpoints. The API key travels only in
// request headers (Authorization: Bearer + x-api-key), never in the URL. Any
// redirect is refused outright (see httpClient): Go strips Authorization on a
// cross-host redirect but copies x-api-key, so following one would replay the
// key to an unvalidated origin.
type Client struct {
	BaseURL     string
	AuthBaseURL string
	HTTP        *http.Client
	APIKey      string
	// Now is the clock for code expiry; nil means time.Now.
	Now func() time.Time
}

// NewClient returns a Client with the production endpoints and a bounded
// timeout. apiKey may be empty for unauthenticated surfaces (device flow).
func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL:     DefaultBaseURL,
		AuthBaseURL: DefaultAuthBaseURL,
		HTTP:        &http.Client{Timeout: 15 * time.Second},
		APIKey:      strings.TrimSpace(apiKey),
	}
}

// APIError is a non-2xx answer from a QG endpoint. Message is already
// redacted against the client's API key before it is stored.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("qg account: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("qg account: HTTP %d: %s", e.StatusCode, e.Message)
}

// doJSON performs an HTTP request against url and decodes the JSON body into
// out, ignoring unknown fields. Auth rides on headers only.
func (c *Client) doJSON(ctx context.Context, method, url string, body io.Reader, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("qg account: build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
		// The gateway accepts either header; sending both keeps older and
		// newer auth paths working without a version negotiation.
		request.Header.Set("x-api-key", c.APIKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("qg account: request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("qg account: read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{
			StatusCode: response.StatusCode,
			Message:    c.redactText(parseErrorMessage(payload)),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("qg account: decode response: %w", err)
	}
	return nil
}

// redactText scrubs the client's API key (and generic secret shapes) out of
// any server-supplied text before it can reach a log or terminal.
func (c *Client) redactText(text string) string {
	options := redaction.Options{}
	if c.APIKey != "" {
		options.ExtraSecretValues = []string{c.APIKey}
	}
	return redaction.RedactString(text, options)
}

// parseErrorMessage extracts a human message from the error shapes the
// gateway and the auth app emit: {"error":{"message":…}}, {"error":…,
// "error_description":…}, or a bare string body.
func parseErrorMessage(payload []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Error) > 0 {
		var nested struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.Error, &nested); err == nil && strings.TrimSpace(nested.Message) != "" {
			return strings.TrimSpace(nested.Message)
		}
		var code string
		if err := json.Unmarshal(envelope.Error, &code); err == nil && code != "" {
			var described struct {
				Description string `json:"error_description"`
			}
			_ = json.Unmarshal(payload, &described)
			if d := strings.TrimSpace(described.Description); d != "" {
				return code + ": " + d
			}
			return code
		}
		return strings.TrimSpace(string(envelope.Error))
	}
	return strings.TrimSpace(string(payload))
}
