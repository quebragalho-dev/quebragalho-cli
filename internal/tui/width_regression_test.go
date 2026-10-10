package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBatchOneLayoutFitsRequiredWidths(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	pet := interactivePetTestModel(t)
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		for _, altScreen := range []bool{false, true} {
			for _, withPet := range []bool{false, true} {
				for _, modelName := range []string{"model-short", "model-" + strings.Repeat("界", 80), ""} {
					t.Run(fmt.Sprintf("%d/alt=%t/pet=%t/model=%d", width, altScreen, withPet, len(modelName)), func(t *testing.T) {
						m := newModel(context.Background(), Options{Cwd: "/workspace/qg", ProviderName: "ollama-cloud", ModelName: modelName, AltScreen: altScreen})
						m.width, m.height = width, 40
						m.gitBranch = "feat/tui-assistant-response-cleanup"
						if withPet {
							m.petID, m.petAnimation, m.petRenderer = pet.petID, pet.petAnimation, pet.petRenderer
						}
						title := plainRender(t, m.titleBar(width))
						divider := plainRender(t, m.composerDividerLine(width))
						empty := plainRender(t, m.emptyState(width))
						for name, block := range map[string]string{"title": title, "divider": divider, "empty": empty, "view": plainRender(t, m.View())} {
							for index, line := range strings.Split(block, "\n") {
								if got := lipgloss.Width(line); got > width {
									t.Fatalf("%s line %d is %d cells, want <=%d: %q", name, index+1, got, width, line)
								}
							}
						}
						if len(strings.Split(title, "\n")) != 2 {
							t.Fatalf("header must keep two lines: %q", title)
						}
						assertNotContains(t, title, "ollama-cloud")
						assertNotContains(t, title, "model-")
						assertNotContains(t, empty, "model-")
						if modelName == "" {
							assertContains(t, divider, "no model")
						} else {
							assertContains(t, divider, "model-")
							if strings.Contains(modelName, "界") {
								assertContains(t, divider, "…")
							}
						}
						if withPet && altScreen {
							if m.petComposerReservedColumns(width) == 0 {
								t.Fatal("fixture must exercise a docked pet")
							}
							if !strings.HasSuffix(divider, strings.Repeat(" ", petReservedColumns)) {
								t.Fatalf("divider invaded pet columns: %q", divider)
							}
						}
					})
				}
			}
		}
	}
}

func TestTitleBarWithoutBranchKeepsWorkspace(t *testing.T) {
	m := model{cwd: "/workspace/qg"}
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		assertContains(t, plainRender(t, m.titleBar(width)), "/workspace/qg")
	}
}

func TestEmptyStateOrientationKeepsContextWithoutModel(t *testing.T) {
	m := model{appVersion: "1.2.3", cwd: "/workspace/qg", gitBranch: "main", modelName: "model-only-at-composer"}
	if got := plainRender(t, m.emptyStateOrientation()); got != "v1.2.3  ·  main" {
		t.Fatalf("orientation = %q", got)
	}
	m.appVersion, m.cwd, m.gitBranch = "", "", ""
	if got := m.emptyStateOrientation(); got != "" {
		t.Fatalf("model alone must not create orientation: %q", got)
	}
}
