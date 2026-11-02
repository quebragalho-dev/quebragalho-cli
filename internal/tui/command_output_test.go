package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFormatCommandOutputRendersSectionsFieldsRowsAndHints(t *testing.T) {
	got := formatCommandOutput(commandOutput{
		Title:  "Model",
		Status: commandStatusOK,
		Sections: []commandSection{
			{
				Title: "Active",
				Fields: []commandField{
					{Key: "provider", Value: "openai"},
					{Key: "model", Value: "gpt-5"},
				},
				Rows: []commandRow{
					{Text: "model shell is ready"},
				},
				Hints: []string{"use /model list to compare options"},
			},
			{
				Title: "Available",
				Rows: []commandRow{
					{Text: "gpt-5"},
					{Text: "claude-sonnet-4.5"},
				},
			},
		},
	})

	want := strings.Join([]string{
		"Model",
		"status: ok",
		"Active",
		"  provider: openai",
		"  model: gpt-5",
		"  - model shell is ready",
		"  hint: use /model list to compare options",
		"Available",
		"  - gpt-5",
		"  - claude-sonnet-4.5",
	}, "\n")

	if got != want {
		t.Fatalf("unexpected command output:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestFormatCommandCardRendersBoundedDashboard(t *testing.T) {
	got := renderCommandCard(commandCard{
		Title:   "Context",
		Summary: []string{"go runtime", "ask permissions", "1 tool"},
		Sections: []commandCardSection{
			{
				Title: "Runtime",
				Fields: []commandField{
					{Key: "cwd", Value: `D:\repo`},
					{Key: "provider", Value: "openai"},
				},
			},
			{
				Title: "Tools",
				Fields: []commandField{
					{Key: "registered", Value: "1"},
				},
			},
		},
		Actions: []string{"/permissions manage access", "/tools inspect catalog"},
	})

	want := strings.Join([]string{
		"Context",
		"go runtime | ask permissions | 1 tool",
		"Runtime",
		"  cwd       D:\\repo",
		"  provider  openai",
		"Tools",
		"  registered  1",
		"actions: /permissions manage access | /tools inspect catalog",
	}, "\n")

	if got != want {
		t.Fatalf("unexpected command card:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestFormatCommandCardRedactsTokenLikeText(t *testing.T) {
	got := renderCommandCard(commandCard{
		Title:   "Context",
		Summary: []string{"provider sk-proj-summary-secret-value"},
		Sections: []commandCardSection{
			{
				Title: "Runtime",
				Fields: []commandField{
					{Key: "api key", Value: "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"},
				},
				Lines: []string{
					"google token AIza1234567890abcdefghijklmnopqrstuvwxyz",
				},
				Rows: []commandRow{
					{Text: "shell used sk-proj-row-secret-value"},
				},
			},
		},
		Actions: []string{"retry with sk-proj-action-secret-value"},
	})

	for _, secret := range []string{
		"sk-proj-summary-secret-value",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"AIza1234567890abcdefghijklmnopqrstuvwxyz",
		"sk-proj-row-secret-value",
		"sk-proj-action-secret-value",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("expected token-like text to be redacted, got:\n%s", got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redacted marker in command card, got:\n%s", got)
	}
}

func TestFormatCommandOutputSupportsAllStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status commandStatus
		want   string
	}{
		{name: "ok", status: commandStatusOK, want: "Status\nstatus: ok"},
		{name: "warning", status: commandStatusWarning, want: "Status\nstatus: warning"},
		{name: "blocked", status: commandStatusBlocked, want: "Status\nstatus: blocked"},
		{name: "info", status: commandStatusInfo, want: "Status\nstatus: info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCommandOutput(commandOutput{
				Title:  "Status",
				Status: tt.status,
			})

			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestFormatCommandOutputRendersPlainLinesAndCommandBullets(t *testing.T) {
	got := formatCommandOutput(commandOutput{
		Title:  "Commands",
		Status: commandStatusInfo,
		Sections: []commandSection{
			{
				Title: "Model",
				Lines: []string{
					"/model [list|id] - Show the active model.",
					commandBullet("gpt-5"),
				},
			},
		},
	})

	want := strings.Join([]string{
		"Commands",
		"status: info",
		"Model",
		"  /model [list|id] - Show the active model.",
		"  - gpt-5",
	}, "\n")

	if got != want {
		t.Fatalf("unexpected command lines:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestFormatCommandOutputRedactsTokenLikeText(t *testing.T) {
	got := formatCommandOutput(commandOutput{
		Title:  "Permissions",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Sandbox grants",
			Lines: []string{
				"bash [allow] - sk-proj-sensitive-token-value approved shell",
				"anthropic: sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
				"google: AIza1234567890abcdefghijklmnopqrstuvwxyz",
			},
		}},
	})

	for _, secret := range []string{
		"sk-proj-sensitive-token-value",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"AIza1234567890abcdefghijklmnopqrstuvwxyz",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("expected token-like text to be redacted, got:\n%s", got)
		}
	}
	if !strings.Contains(got, "[REDACTED] approved shell") {
		t.Fatalf("expected token-like text to be redacted, got:\n%s", got)
	}
}

func TestFormatCommandOutputCompactsWhitespaceAndSkipsEmptyItems(t *testing.T) {
	got := formatCommandOutput(commandOutput{
		Title:  "  Doctor  ",
		Status: commandStatusWarning,
		Sections: []commandSection{
			{
				Title: " ",
				Fields: []commandField{
					{Key: " ", Value: "skip me"},
					{Key: "sandbox", Value: "  danger-full-access\r\n(no prompt)  "},
				},
				Rows: []commandRow{
					{Text: " "},
					{Text: "checks complete\nneeds review"},
				},
				Hints: []string{
					"  use /doctor for details\r\nsoon  ",
				},
			},
		},
		Hints: []string{
			"  top-level hint\nis compacted  ",
		},
	})

	want := strings.Join([]string{
		"Doctor",
		"status: warning",
		"  sandbox: danger-full-access (no prompt)",
		"  - checks complete needs review",
		"  hint: use /doctor for details soon",
		"hint: top-level hint is compacted",
	}, "\n")

	if got != want {
		t.Fatalf("unexpected compact command output:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestFormatCommandCardAlignsAccentedKeysByCellWidth(t *testing.T) {
	got := renderCommandCard(commandCard{
		Title: "Nível",
		Sections: []commandCardSection{{
			Title: "Estado",
			Fields: []commandField{
				{Key: "nível ativo", Value: "baixo"},
				{Key: "modelo", Value: "nenhum"},
				{Key: "disponíveis", Value: "baixo, médio"},
			},
		}},
	})
	lines := strings.Split(got, "\n")
	positions := map[string]int{}
	for _, line := range lines {
		for _, key := range []string{"nível ativo", "modelo", "disponíveis"} {
			if strings.Contains(line, key) {
				value := map[string]string{"nível ativo": "baixo", "modelo": "nenhum", "disponíveis": "baixo, médio"}[key]
				at := strings.Index(line, value)
				if at < 0 {
					t.Fatalf("missing value %q in %q", value, line)
				}
				prefix := line[:at]
				positions[key] = lipgloss.Width(prefix)
			}
		}
	}
	if len(positions) != 3 {
		t.Fatalf("expected 3 aligned rows, got:\n%s", got)
	}
	first := positions["nível ativo"]
	for _, at := range positions {
		if at != first {
			t.Fatalf("value column misaligned: %v in:\n%s", positions, got)
		}
	}
}
