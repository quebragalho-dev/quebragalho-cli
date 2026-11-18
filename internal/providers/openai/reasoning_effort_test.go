package openai

import "testing"

func TestOpenAIReasoningEffortPreservesKnownProviderTiers(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
		if got := openAIReasoningEffort(effort); got != effort {
			t.Fatalf("openAIReasoningEffort(%q) = %q", effort, got)
		}
	}
	if got := openAIReasoningEffort("unknown"); got != "" {
		t.Fatalf("unknown effort = %q, want omitted", got)
	}
}
