package providerio

import (
	"net/http"
	"strings"
	"unicode"
)

type AuthHeaders struct {
	APIKey            string
	DefaultAuthHeader string
	DefaultAuthScheme string
	AuthHeader        string
	AuthScheme        string
	AuthHeaderValue   string
	CustomHeaders     map[string]string
}

func ApplyAuthHeaders(request *http.Request, options AuthHeaders) {
	for key, value := range options.CustomHeaders {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		request.Header.Set(key, strings.TrimSpace(value))
	}

	header := strings.TrimSpace(options.AuthHeader)
	customHeader := header != ""
	if header == "" {
		header = strings.TrimSpace(options.DefaultAuthHeader)
	}
	if header == "" {
		return
	}

	value := strings.TrimSpace(options.AuthHeaderValue)
	if value == "" {
		apiKey := strings.TrimSpace(options.APIKey)
		if apiKey == "" {
			return
		}
		scheme := strings.TrimSpace(options.AuthScheme)
		if !customHeader && scheme == "" {
			scheme = strings.TrimSpace(options.DefaultAuthScheme)
		}
		if scheme != "" && !strings.EqualFold(scheme, "none") && !strings.EqualFold(scheme, "raw") {
			value = scheme + " " + apiKey
		} else {
			value = apiKey
		}
	}
	request.Header.Set(header, value)
}

type SessionHeader struct {
	name string
}

func NewSessionHeader(name string) SessionHeader {
	return SessionHeader{name: strings.TrimSpace(name)}
}

func (header SessionHeader) Apply(request *http.Request, sessionID string) {
	if header.name == "" {
		return
	}
	// The gateway contract rejects values over 128 bytes and values with
	// leading/trailing space or control bytes; the server ignores such a
	// header silently, so an invalid value is simply not sent — never
	// replaced with an invented one.
	if !validConversationID(sessionID) {
		return
	}
	request.Header.Set(header.name, sessionID)
}

// validConversationID reports whether value may ride a session-affinity
// header: printable text, at most 128 bytes, no surrounding space, no
// control characters.
func validConversationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	if strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func CopyHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	copied := make(map[string]string, len(headers))
	for key, value := range headers {
		copied[key] = value
	}
	return copied
}
