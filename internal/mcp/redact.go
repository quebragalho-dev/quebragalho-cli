package mcp

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// mcpURLPattern finds http(s) URLs inside error text. net/http errors echo
// the request URL, so a ${VAR} expanded into a query string would otherwise
// leak the secret into skipped reasons, check/list output, and model errors.
var mcpURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// sanitizeMCPErrorText redacts every URL in free-form error text to
// scheme://host/path, dropping userinfo, query, and fragment where expanded
// secrets travel. Use it on any error that reaches the user, the transcript,
// or the model.
func sanitizeMCPErrorText(text string) string {
	if !strings.Contains(text, "http") {
		return text
	}
	return mcpURLPattern.ReplaceAllStringFunc(text, redactURLForError)
}

func redactURLForError(raw string) string {
	parsed, err := url.Parse(strings.TrimRight(raw, ".,;!)"))
	trail := raw[len(strings.TrimRight(raw, ".,;!)")):]
	if err != nil || parsed.Host == "" {
		return "[URL redactada]" + trail
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if out := parsed.String(); out != "" {
		return out + trail
	}
	return "[URL redactada]" + trail
}

// sanitizedMCPRequestError rewrites a request failure so the echoed URL
// cannot carry an expanded secret: userinfo and query are stripped, the
// transport error itself is preserved for errors.As/Is matching.
func sanitizedMCPRequestError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	redacted := redactURLForError(urlErr.URL)
	if redacted == urlErr.URL {
		return err
	}
	return &url.Error{Op: urlErr.Op, URL: redacted, Err: urlErr.Err}
}
