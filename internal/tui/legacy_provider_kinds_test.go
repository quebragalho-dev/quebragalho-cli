package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

// Characterization: ad-hoc descriptors (Transport "") keep the original
// fallback — "" without formats, the first format otherwise. setupProviderDescriptor
// (onboarding.go) builds these when the option ID is not in the catalog, and
// normalizeProfileForProvider (picker.go) / session setup write the result
// into profile.APIFormat.
func TestLegacyAdHocDescriptorAPIFormatFallback(t *testing.T) {
	empty := providercatalog.Descriptor{ID: "custom-x"}
	if got := providerWizardAPIFormat(empty); got != "" {
		t.Fatalf("providerWizardAPIFormat(empty transport, no formats) = %q, want empty", got)
	}
	withFormats := providercatalog.Descriptor{
		ID:                  "custom-x",
		SupportedAPIFormats: []providercatalog.APIFormat{"first-format", "second-format"},
	}
	if got := providerWizardAPIFormat(withFormats); got != "first-format" {
		t.Fatalf("providerWizardAPIFormat(empty transport, with formats) = %q, want first-format", got)
	}
	openaiCompat := providercatalog.Descriptor{
		ID:                  "custom-x",
		Transport:           providercatalog.TransportOpenAICompatible,
		SupportedAPIFormats: []providercatalog.APIFormat{"first-format"},
	}
	if got := providerWizardAPIFormat(openaiCompat); got != string(providercatalog.APIFormatOpenAIChatCompletions) {
		t.Fatalf("providerWizardAPIFormat(openai-compatible) = %q, want chat-completions", got)
	}
}

// Characterization: the default arm maps an empty/unknown transport to the
// lowercased transport string ("" for ad-hoc descriptors), exactly as before.
func TestLegacyAdHocDescriptorKindDefault(t *testing.T) {
	if got := providerWizardProviderKind(providercatalog.Descriptor{ID: "custom-x"}); got != "" {
		t.Fatalf("providerWizardProviderKind(empty transport) = %q, want empty", got)
	}
	qg, err := providercatalog.Require(providercatalog.QGID)
	if err != nil {
		t.Fatalf("Require(qg) = %v", err)
	}
	if got := providerWizardProviderKind(qg); got != config.ProviderKindOpenAICompatible {
		t.Fatalf("providerWizardProviderKind(qg) = %q, want openai-compatible", got)
	}
}
