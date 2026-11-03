package errhint

import (
	"errors"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want Category
	}{
		{"nil-ish empty", "", Unknown},
		{"providerio auth prefix", "auth error: your API key is missing or invalid — run `qg auth`", Auth},
		{"raw 401", "provider request error: 401 Unauthorized", Auth},
		{"invalid api key", "provider request error: invalid_api_key: incorrect key provided", Auth},
		{"rate limit prefix", "rate limit error: 429 too many requests", RateLimit},
		{"overloaded", "provider error: model is overloaded, please retry", RateLimit},
		{"resource exhausted gemini", "provider stream error: rpc error: code = ResourceExhausted desc = quota exceeded", RateLimit},
		{"context length openai", "provider request error: this model's maximum context length is 128000 tokens", ContextOverflow},
		{"context_length_exceeded", "provider request error: context_length_exceeded", ContextOverflow},
		{"model not found", "provider request error: 404 the model `gpt-9` does not exist", ModelNotFound},
		{"unknown model", "provider request error: unknown model: sonnet-99", ModelNotFound},
		{"model not allowed for account", "provider request error: model not allowed for this account", ModelNotFound},
		{"dns failure", "provider stream error: Post \"https://api...\": dial tcp: lookup api.foo.com: no such host", Connectivity},
		{"connection refused", "provider stream error: dial tcp 127.0.0.1:443: connection refused", Connectivity},
		{"deadline", "provider stream error: context deadline exceeded (Client.Timeout exceeded)", Connectivity},
		{"provider-marked but no sub-signature", "provider error: something totally unexpected happened", Unknown},

		// An incidental number that merely contains a status-code digit run must NOT
		// be mis-bucketed (digit-boundary check), even behind a provider marker.
		{"incidental latency number", "provider error: request completed in 4290ms then failed", Unknown},
		{"incidental request id", "provider error: request id 14015 failed unexpectedly", Unknown},
		{"standalone 401 still auth", "provider request error: got 401 from upstream", Auth},
		{"standalone 429 still rate limit", "provider error: 429 returned", RateLimit},

		// Local (non-provider) failures must NOT be classified — no provider marker,
		// so no bogus recovery hint attaches to them.
		{"local fs permission denied", "open /etc/shadow: permission denied", Unknown},
		{"local file missing", "stat foo.go: no such file or directory", Unknown},
		{"local model config typo", "unknown model: sonnet-99", Unknown},
		{"tool timeout local", "tool bash timed out after 600s", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("Classify(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestClassifyNil(t *testing.T) {
	if got := Classify(nil); got != Unknown {
		t.Fatalf("Classify(nil) = %v, want Unknown", got)
	}
}

// Context-overflow must win over the connectivity "timeout" catch-all: a message
// mentioning both "context length" and a timeout is an overflow, not a network
// problem.
func TestContextOverflowBeatsConnectivity(t *testing.T) {
	err := errors.New("provider request error: maximum context length exceeded; request timeout")
	if got := Classify(err); got != ContextOverflow {
		t.Fatalf("Classify = %v, want ContextOverflow", got)
	}
}

func TestModelNotAllowedHintIsModelUnavailable(t *testing.T) {
	// A gateway 403 with code model_not_allowed reaches errhint as a provider
	// request error (providerio strips the numeric code and the auth prefix),
	// so it must bucket as ModelNotFound, not Auth.
	denied := errors.New("provider request error: model not allowed for this account")
	if got := Classify(denied); got != ModelNotFound {
		t.Fatalf("Classify = %v, want ModelNotFound", got)
	}
	// Reference shape for the same category: both hints must match it exactly,
	// so the test survives hint rewording and i18n.
	known := errors.New("provider request error: model does not exist")
	if got := Classify(known); got != ModelNotFound {
		t.Fatalf("Classify(reference) = %v, want ModelNotFound", got)
	}
	if got, want := TUIHint(denied), TUIHint(known); got == "" || got != want {
		t.Fatalf("TUIHint(denied) = %q, want reference hint %q", got, want)
	}
	if got, want := CLIHint(denied), CLIHint(known); got == "" || got != want {
		t.Fatalf("CLIHint(denied) = %q, want reference hint %q", got, want)
	}
	// Must not collapse into the key/auth hint on either surface.
	authErr := errors.New("auth error: bad key")
	if got, auth := TUIHint(denied), TUIHint(authErr); got == auth {
		t.Fatalf("TUIHint(denied) = %q, must differ from auth hint", got)
	}
	if got, auth := CLIHint(denied), CLIHint(authErr); got == auth {
		t.Fatalf("CLIHint(denied) = %q, must differ from auth hint", got)
	}
}

func TestHintsPresentForKnownCategories(t *testing.T) {
	known := []error{
		errors.New("auth error: bad key"),
		errors.New("rate limit error: 429"),
		errors.New("provider stream error: dial tcp: no such host"),
		errors.New("provider request error: model does not exist"),
		errors.New("provider request error: maximum context length is 128000 tokens"),
	}
	for _, err := range known {
		if h := TUIHint(err); strings.TrimSpace(h) == "" {
			t.Fatalf("TUIHint(%q) empty, want a hint", err)
		}
		if h := CLIHint(err); strings.TrimSpace(h) == "" {
			t.Fatalf("CLIHint(%q) empty, want a hint", err)
		}
	}
	// Unknown yields no hint on either surface.
	unknown := errors.New("provider error: mystery")
	if h := TUIHint(unknown); h != "" {
		t.Fatalf("TUIHint(unknown) = %q, want empty", h)
	}
	if h := CLIHint(unknown); h != "" {
		t.Fatalf("CLIHint(unknown) = %q, want empty", h)
	}
}

// The human Auth hint must render the env-var conjunction in Portuguese while
// the canonical constant stays English: reverting keyEnvHintHuman to the raw
// constant (or editing KeyEnvHint itself) must fail here.
func TestCLIHintAuthRendersTranslatedEnvConjunction(t *testing.T) {
	if got := providercatalog.KeyEnvHint; got != "QG_API_KEY (or QUEBRAGALHO_API_KEY)" {
		t.Fatalf("KeyEnvHint = %q, want canonical English", got)
	}
	hint := CLIHint(errors.New("auth error: bad key"))
	if !strings.Contains(hint, "(ou QUEBRAGALHO_API_KEY)") {
		t.Fatalf("CLIHint auth = %q, want translated conjunction", hint)
	}
	if strings.Contains(hint, "(or QUEBRAGALHO_API_KEY)") {
		t.Fatalf("CLIHint auth leaked canonical conjunction: %q", hint)
	}
}
