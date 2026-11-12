package mcp

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
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

// Redact trades every non-empty ${VAR} value this server substituted back
// for its ${VAR} reference, longest values first. Use it on any text that
// leaves the package toward the user, the transcript, or the model.
func (server Server) Redact(text string) string {
	if len(server.Secrets) == 0 {
		return text
	}
	names := make([]string, 0, len(server.Secrets))
	for name := range server.Secrets {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool {
		return len(server.Secrets[names[left]]) > len(server.Secrets[names[right]])
	})
	for _, name := range names {
		value := server.Secrets[name]
		if value == "" {
			continue
		}
		text = strings.ReplaceAll(text, value, "${"+name+"}")
	}
	return text
}

// redactedError carries an error whose text had expanded secrets traded back
// for ${VAR} references. It unwraps to the original so errors.As keeps
// working across the redaction.
type redactedError struct {
	text string
	err  error
}

func (err *redactedError) Error() string { return err.text }
func (err *redactedError) Unwrap() error { return err.err }

// redactError trades a server's substituted values out of an error message,
// preserving the error chain for errors.As.
func redactError(server Server, err error) error {
	if err == nil {
		return nil
	}
	redacted := server.Redact(err.Error())
	if redacted == err.Error() {
		return err
	}
	return &redactedError{text: redacted, err: err}
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
