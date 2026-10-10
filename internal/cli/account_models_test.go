package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

func strPtr(value string) *string { return &value }
func int64Ptr(value int64) *int64 { return &value }

func TestRunModelsJSONEnrichesQGPricingLive(t *testing.T) {
	fake := &fakeQGAccount{models: []qgaccount.ModelInfo{
		{
			ID:              "muse-spark-1.3-contributor",
			OwnedBy:         "QG",
			DisplayName:     strPtr("Muse Spark"),
			ContextLength:   int64Ptr(1000000),
			MaxOutputTokens: nil, // gateway null: must stay null, never 0
			Capabilities:    qgaccount.ModelCapabilities{Tools: true, Vision: false, Reasoning: true},
			AcceptsBonus:    true,
			Pricing:         qgaccount.ModelPricing{Currency: "USD", Input: "0.10", Output: "0.40", CachedInput: "0.01"},
			PricingBRL:      qgaccount.ModelPricing{Currency: "BRL", Input: "0.5", Output: "2.0", CachedInput: "0.05"},
			Offer:           nil,
		},
	}}
	deps := accountDeps(t, fake)

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"models", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON decode: %v\n%s", err, stdout.String())
	}
	var entry map[string]any
	for _, model := range payload.Models {
		if model["id"] == "muse-spark-1.3-contributor" {
			entry = model
		}
	}
	if entry == nil {
		t.Fatalf("model missing from JSON:\n%s", stdout.String())
	}
	if entry["accepts_bonus"] != true {
		t.Fatalf("accepts_bonus = %v, want true", entry["accepts_bonus"])
	}
	if entry["max_output_tokens"] != nil {
		t.Fatalf("max_output_tokens = %v, want null (gateway sent null)", entry["max_output_tokens"])
	}
	pricing, ok := entry["pricing"].(map[string]any)
	if !ok || pricing["input"] != "0.10" {
		t.Fatalf("pricing = %v", entry["pricing"])
	}
	pricingBRL, ok := entry["pricing_brl"].(map[string]any)
	if !ok || pricingBRL["currency"] != "BRL" {
		t.Fatalf("pricing_brl = %v", entry["pricing_brl"])
	}
	if entry["offer"] != nil {
		t.Fatalf("offer = %v, want null", entry["offer"])
	}
	// The registry snapshot keeps its capability name list; the live gateway
	// booleans ride in the text output, not under the colliding key.
	capabilities, ok := entry["capabilities"].([]any)
	if !ok || len(capabilities) == 0 {
		t.Fatalf("capabilities = %v, want the snapshot name list preserved", entry["capabilities"])
	}
}

func TestRunModelsTextShowsPricingPerMillion(t *testing.T) {
	fake := &fakeQGAccount{models: []qgaccount.ModelInfo{
		{
			ID:           "muse-spark-1.3-contributor",
			Pricing:      qgaccount.ModelPricing{Currency: "USD", Input: "0.10", Output: "0.40"},
			PricingBRL:   qgaccount.ModelPricing{Currency: "BRL", Input: "0.50", Output: "2.00"},
			AcceptsBonus: true,
			Offer:        &qgaccount.ModelOffer{Type: "relampago", DiscountPercent: 20},
		},
	}}
	deps := accountDeps(t, fake)

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"models"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"0.10", "0.50", "bônus", "relampago"} {
		if !strings.Contains(out, want) {
			t.Fatalf("models text missing %q:\n%s", want, out)
		}
	}
}

func TestRunModelsDegradesWithoutKey(t *testing.T) {
	fake := &fakeQGAccount{modelsErr: &qgaccount.APIError{StatusCode: 401, Message: "invalid api key"}}
	deps := accountDeps(t, fake)

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"models", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "models") {
		t.Fatalf("static catalog missing when live enrichment fails:\n%s", stdout.String())
	}
}

// The live capability booleans and description have no JSON home (the registry
// snapshot already owns the "capabilities" key), so the text suffix is where
// they must surface — otherwise the gateway fields are decoded and dropped.
func TestRunModelsTextShowsCapabilitiesAndDescription(t *testing.T) {
	fake := &fakeQGAccount{models: []qgaccount.ModelInfo{
		{
			ID:           "muse-spark-1.3-contributor",
			Capabilities: qgaccount.ModelCapabilities{Tools: true, Vision: false, Reasoning: true},
			Description:  strPtr("Modelo de código da QG"),
		},
	}}
	deps := accountDeps(t, fake)

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"models"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "recursos: tools, reasoning") {
		t.Fatalf("capabilities missing (or a false flag printed):\n%s", out)
	}
	if !strings.Contains(out, "Modelo de código da QG") {
		t.Fatalf("description missing:\n%s", out)
	}
}

// With no key configured, `qg models` still prints the static registry and
// exits 0 — and the failed key resolution must stay off stderr entirely, since
// this command degrades gracefully instead of failing.
//
// The failing injected client is what keeps this hermetic: empty deps would
// let fillAppDeps resolve a real key (config file / QG_API_KEY) and dial the
// gateway, which is exactly the network call this test must not make.
func TestRunModelsDegradesQuietlyWithoutKey(t *testing.T) {
	deps := appDeps{
		newQGAccountClient: func() (qgAccountAPI, error) {
			return nil, errors.New("network disabled in tests")
		},
	}
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"models"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("quiet resolution leaked to stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Models") {
		t.Fatalf("static catalog missing:\n%s", stdout.String())
	}
}
