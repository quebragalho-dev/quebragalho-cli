package cli

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

// Characterization: building a profile from the (single) catalog descriptor
// yields an openai-compatible profile with the chat-completions format.
// Deleting dead transports must not change this.
func TestLegacyCatalogDescriptorBuildsOpenAICompatibleProfile(t *testing.T) {
	descriptor, err := providercatalog.Require(providercatalog.QGID)
	if err != nil {
		t.Fatalf("Require(qg) = %v", err)
	}
	if got := providerKindForDescriptor(descriptor); got != config.ProviderKindOpenAICompatible {
		t.Fatalf("providerKindForDescriptor(qg) = %q, want openai-compatible", got)
	}
	if got := firstAPIFormat(descriptor); got != string(providercatalog.APIFormatOpenAIChatCompletions) {
		t.Fatalf("firstAPIFormat(qg) = %q, want chat-completions", got)
	}
}

// Characterization: ad-hoc descriptors (Transport "") keep the original
// fallback — "" without formats, the first format otherwise. setupProviderDescriptor
// (tui/onboarding.go) builds these when the option ID is not in the catalog,
// and the result is written into profile.APIFormat.
func TestLegacyAdHocDescriptorAPIFormatFallback(t *testing.T) {
	empty := providercatalog.Descriptor{ID: "custom-x"}
	if got := firstAPIFormat(empty); got != "" {
		t.Fatalf("firstAPIFormat(empty transport, no formats) = %q, want empty", got)
	}
	withFormats := providercatalog.Descriptor{
		ID:                  "custom-x",
		SupportedAPIFormats: []providercatalog.APIFormat{"first-format", "second-format"},
	}
	if got := firstAPIFormat(withFormats); got != "first-format" {
		t.Fatalf("firstAPIFormat(empty transport, with formats) = %q, want first-format", got)
	}
	openaiCompat := providercatalog.Descriptor{
		ID:                  "custom-x",
		Transport:           providercatalog.TransportOpenAICompatible,
		SupportedAPIFormats: []providercatalog.APIFormat{"first-format"},
	}
	if got := firstAPIFormat(openaiCompat); got != string(providercatalog.APIFormatOpenAIChatCompletions) {
		t.Fatalf("firstAPIFormat(openai-compatible) = %q, want chat-completions", got)
	}
}

// Characterization: the default arm maps an empty/unknown transport to the
// lowercased transport string ("" for ad-hoc descriptors), exactly as before.
func TestLegacyAdHocDescriptorKindDefault(t *testing.T) {
	if got := providerKindForDescriptor(providercatalog.Descriptor{ID: "custom-x"}); got != "" {
		t.Fatalf("providerKindForDescriptor(empty transport) = %q, want empty", got)
	}
}
