package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestProviderCatalogRuntimeProvidersShowOnboardingSetupHints(t *testing.T) {
	output := runProviderCatalogOnboarding(t)

	tests := []struct {
		id           string
		name         string
		transport    string
		defaultModel string
	}{
		{id: "qg", name: "Quebra Galho", transport: "openai-compatible", defaultModel: "muse-spark-1.3-contributor"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			block := providerCatalogOnboardingBlock(t, output, tt.id)
			nameLine := "name=" + tt.name
			if strings.Contains(tt.name, " ") {
				nameLine = "name=" + strconv.Quote(tt.name)
			}
			for _, want := range []string{
				"id=" + tt.id,
				nameLine,
				"transport=" + tt.transport,
				"defaultModel=" + tt.defaultModel,
				"setup: qg providers setup " + tt.id + " --set-active",
			} {
				if !strings.Contains(block, want) {
					t.Fatalf("expected %s catalog block to contain %q, got:\n%s", tt.id, want, block)
				}
			}
			if strings.Contains(block, "unsupported:") {
				t.Fatalf("runtime-supported provider %s should not show unsupported reason, got:\n%s", tt.id, block)
			}
		})
	}
}

func TestProviderCatalogQGRequiresAPIKey(t *testing.T) {
	output := runProviderCatalogOnboarding(t)
	block := providerCatalogOnboardingBlock(t, output, "qg")

	for _, want := range []string{
		"requiresAuth=true",
		"authEnvVars=QUEBRAGALHO_API_KEY",
		"setup: qg providers setup qg --set-active",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("expected qg catalog block to contain %q, got:\n%s", want, block)
		}
	}
}

func runProviderCatalogOnboarding(t *testing.T) string {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDeps([]string{"providers", "catalog"}, &stdout, &stderr, providerCatalogDeps(t))
	if exitCode != exitSuccess {
		t.Fatalf("expected exit code %d, got %d: %s", exitSuccess, exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	return stdout.String()
}

func providerCatalogOnboardingBlock(t *testing.T, output string, id string) string {
	t.Helper()

	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	// A recommended row renders as `★ id=...`, so match the token anywhere in
	// the line rather than at its start.
	start := -1
	for index, line := range lines {
		if strings.Contains(line, "id="+id) {
			start = index
			break
		}
	}
	if start == -1 {
		t.Fatalf("catalog block for provider %q not found in:\n%s", id, output)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if strings.Contains(lines[index], "id=") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
