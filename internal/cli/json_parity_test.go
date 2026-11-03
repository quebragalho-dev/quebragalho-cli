package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providerhealth"
)

// Machine output must stay in canonical English. These tests pin the JSON
// payloads of providers check and mcp check to the base strings, so a
// Portuguese-only change to the human branch cannot leak into --json.

func decodeProvidersCheckJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode providers check --json %q: %v", out, err)
	}
	return payload
}

func providersCheckNextActionsJSON(t *testing.T, payload map[string]any) []string {
	t.Helper()
	raw, ok := payload["nextActions"]
	if !ok {
		t.Fatalf("missing nextActions in %v", payload)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("nextActions is not a list in %v", payload)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("nextAction is not a string in %v", payload)
		}
		out = append(out, text)
	}
	return out
}

func TestProvidersCheckJSONStaysCanonicalEnglish(t *testing.T) {
	t.Run("no connectivity", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		deps := commandCenterDeps(t)
		if code := runWithDeps([]string{"providers", "check", "work", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		payload := decodeProvidersCheckJSON(t, stdout.String())
		got := providersCheckNextActionsJSON(t, payload)
		want := []string{"run qg providers check work --connectivity"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("nextActions = %q, want %q", got, want)
		}
		if strings.Contains(stdout.String(), "rode ") || strings.Contains(stdout.String(), "próximo") || strings.Contains(stdout.String(), "provedor pronto") {
			t.Fatalf("providers check --json leaked Portuguese: %q", stdout.String())
		}
	})

	t.Run("connectivity ok", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		deps := commandCenterDeps(t)
		deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
			return providerhealth.Result{
				Status: providerhealth.StatusPass,
				Checks: []providerhealth.Check{{ID: "provider.connectivity", Status: providerhealth.StatusPass, Message: "reachable"}},
			}
		}
		if code := runWithDeps([]string{"providers", "check", "work", "--json", "--connectivity"}, &stdout, &stderr, deps); code != exitSuccess {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		payload := decodeProvidersCheckJSON(t, stdout.String())
		got := providersCheckNextActionsJSON(t, payload)
		want := []string{`run qg exec "hello" --model gpt-4.1`}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("nextActions = %q, want %q", got, want)
		}
	})

	t.Run("connectivity warn", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		deps := commandCenterDeps(t)
		deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
			return providerhealth.Result{
				Status: providerhealth.StatusWarn,
				Checks: []providerhealth.Check{{ID: "provider.connectivity", Status: providerhealth.StatusWarn, Message: "slow"}},
			}
		}
		if code := runWithDeps([]string{"providers", "check", "work", "--json", "--connectivity"}, &stdout, &stderr, deps); code != exitSuccess {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		payload := decodeProvidersCheckJSON(t, stdout.String())
		got := providersCheckNextActionsJSON(t, payload)
		want := []string{"review the warning, then rerun qg providers check work --connectivity"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("nextActions = %q, want %q", got, want)
		}
		if strings.Contains(stdout.String(), "reveja o aviso") {
			t.Fatalf("providers check --json leaked Portuguese: %q", stdout.String())
		}
	})

	t.Run("connectivity fail", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		deps := commandCenterDeps(t)
		deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
			profile := config.ProviderProfile{
				Name:         "local",
				ProviderKind: config.ProviderKindOpenAICompatible,
				BaseURL:      "https://api.example.com/v1",
				APIKey:       "sk-test-secret",
				Model:        "custom-model",
			}
			return config.ResolvedConfig{ActiveProvider: "local", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
		}
		deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
			return providerhealth.Result{
				Status: providerhealth.StatusFail,
				Checks: []providerhealth.Check{{ID: "provider.connectivity", Status: providerhealth.StatusFail, Message: "boom"}},
			}
		}
		if code := runWithDeps([]string{"providers", "check", "local", "--json", "--connectivity"}, &stdout, &stderr, deps); code != exitProvider {
			t.Fatalf("exit = %d, want %d, stderr = %q", code, exitProvider, stderr.String())
		}
		payload := decodeProvidersCheckJSON(t, stdout.String())
		got := providersCheckNextActionsJSON(t, payload)
		want := []string{"verify the API key, base URL, and model, then rerun qg providers check local --connectivity"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("nextActions = %q, want %q", got, want)
		}
		if strings.Contains(stdout.String(), "confira a chave") {
			t.Fatalf("providers check --json leaked Portuguese: %q", stdout.String())
		}
	})

	t.Run("connectivity fail keyless", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		deps := commandCenterDeps(t)
		deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
			profile := providerCheckGuidanceProfile()
			return config.ResolvedConfig{ActiveProvider: "qg", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
		}
		deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
			return providerhealth.Result{
				Status: providerhealth.StatusFail,
				Checks: []providerhealth.Check{{ID: "provider.auth", Status: providerhealth.StatusFail, Message: "boom"}},
			}
		}
		if code := runWithDeps([]string{"providers", "check", "qg", "--json", "--connectivity"}, &stdout, &stderr, deps); code != exitProvider {
			t.Fatalf("exit = %d, want %d, stderr = %q", code, exitProvider, stderr.String())
		}
		payload := decodeProvidersCheckJSON(t, stdout.String())
		got := providersCheckNextActionsJSON(t, payload)
		want := []string{"set QG_API_KEY (or QUEBRAGALHO_API_KEY) in your environment or run `qg login`, then rerun qg providers check qg --connectivity"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("nextActions = %q, want %q", got, want)
		}
		if strings.Contains(stdout.String(), "(ou ") || strings.Contains(stdout.String(), "rode ") {
			t.Fatalf("providers check --json leaked Portuguese: %q", stdout.String())
		}
	})
}

func TestMCPCheckJSONStaysCanonicalEnglish(t *testing.T) {
	setTrustConfigRoot(t)
	deps := appDeps{
		getwd: func() (string, error) { return t.TempDir(), nil },
		resolveMCPConfig: func(_ string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"broken": {Type: "stdio", Command: "qg-definitely-not-a-real-binary-xyz"},
			}}, nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runWithDeps([]string{"mcp", "check", "broken", "--json"}, &out, &errBuf, deps); code == exitSuccess {
		t.Fatalf("mcp check --json must fail, stdout=%q stderr=%q", out.String(), errBuf.String())
	}
	var payload struct {
		ServerName string `json:"serverName"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode mcp check --json %q: %v", out.String(), err)
	}
	if payload.Status != "unreachable" {
		t.Fatalf("status = %q, want unreachable", payload.Status)
	}
	if !strings.Contains(payload.Error, "MCP server broken is not reachable") {
		t.Fatalf("error = %q, want canonical English", payload.Error)
	}
	if strings.Contains(payload.Error, "não está acessível") {
		t.Fatalf("mcp check --json leaked Portuguese: %q", payload.Error)
	}
}
