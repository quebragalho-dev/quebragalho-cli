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
