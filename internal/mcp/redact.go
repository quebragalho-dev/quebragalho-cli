package mcp

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// minRedactableSecretLen is the shortest substituted value worth trading
// back for its ${VAR} reference. Short values (a mode, a port, a boolean)
// occur naturally in text and must never rewrite tool data; only real
// secrets travel this path.
const minRedactableSecretLen = 8

// Redact trades every ${VAR} value this server substituted back for its
// ${VAR} reference, longest values first, including the URL-escaped forms
// net/http errors carry. Only error, status, and list/check text goes
// through here — never a successful tool result, whose content belongs to
// the model verbatim.
func (server Server) Redact(text string) string {
	if len(server.Secrets) == 0 || text == "" {
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
		if len(value) < minRedactableSecretLen {
			continue
		}
		for _, form := range []string{value, url.PathEscape(value), url.QueryEscape(value)} {
			if form == "" {
				continue
			}
			text = strings.ReplaceAll(text, form, "${"+name+"}")
		}
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

// mcpVarRefPattern finds the ${NAME} references Redact trades expanded
// secrets back for, so the URL sanitizer can shelter them from encoding.
var mcpVarRefPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

func redactURLForError(raw string) string {
	// A ${VAR} reference traded back by Redact must survive the
	// parse/serialize round-trip below: url.String percent-encodes braces,
	// which would turn the readable reference into $%7B...%7D. Shelter the
	// references behind URL-safe placeholders and restore them after.
	sheltered := raw
	var refs []string
	if !strings.Contains(raw, "__QGREF__") {
		sheltered = mcpVarRefPattern.ReplaceAllStringFunc(raw, func(match string) string {
			refs = append(refs, match)
			return "__QGREF__" + strconv.Itoa(len(refs)-1) + "__"
		})
	}
	parsed, err := url.Parse(strings.TrimRight(sheltered, ".,;!)"))
	trail := sheltered[len(strings.TrimRight(sheltered, ".,;!)")):]
	if err != nil || parsed.Host == "" {
		return "[URL redactada]" + trail
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	out := parsed.String()
	if strings.TrimSpace(out) == "" {
		return "[URL redactada]" + trail
	}
	for index, ref := range refs {
		out = strings.ReplaceAll(out, "__QGREF__"+strconv.Itoa(index)+"__", ref)
	}
	return out + trail
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
