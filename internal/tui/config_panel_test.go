package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func configPanelTestModel() model {
	m := newModel(context.Background(), Options{
		ProviderName:  "qg",
		ModelName:     "muse-spark-1.3-contributor",
		RecapsEnabled: true,
		AgentOptions:  agent.Options{MaxTurns: 80},
	})
	m.responseStyle = defaultedResponseStyle("")
	return m
}

func openConfigPanel(t *testing.T, m model) model {
	t.Helper()
	updated, cmd := m.dispatchCommand(parseCommand("/config"))
	if cmd != nil {
		t.Fatal("expected /config to open without a background command")
	}
	next := updated.(model)
	if next.picker == nil || next.picker.kind != pickerConfig {
		t.Fatalf("expected /config to open the settings panel, got %+v", next.picker)
	}
	return next
}

func findConfigRow(t *testing.T, m model, value string) pickerItem {
	t.Helper()
	for _, item := range m.picker.items {
		if item.Value == value {
			return item
		}
	}
	t.Fatalf("config panel missing row %q", value)
	return pickerItem{}
}

// /config abre o painel único com as seções da v1 e os valores reais da sessão.
func TestConfigPanelOpensWithRealValues(t *testing.T) {
	m := openConfigPanel(t, configPanelTestModel())

	if title := strings.TrimSpace(m.picker.title); title != "Configurações" {
		t.Fatalf("panel title wrong: %q", title)
	}
	if len(m.picker.items) != 11 {
		t.Fatalf("expected 11 config rows, got %d", len(m.picker.items))
	}
	wantGroups := []string{"MODELO", "EXECUÇÃO", "VOZ", "APARÊNCIA", "CONEXÕES"}
	seen := map[string]bool{}
	var order []string
	for _, item := range m.picker.items {
		if !seen[item.Group] {
			seen[item.Group] = true
			order = append(order, item.Group)
		}
		if strings.TrimSpace(item.Label) == "" || strings.TrimSpace(item.Meta) == "" {
			t.Fatalf("row %q has empty label or value", item.Value)
		}
	}
	if strings.Join(order, ",") != strings.Join(wantGroups, ",") {
		t.Fatalf("section order wrong: %v", order)
	}
	wantMeta := map[string]string{
		configRowModel:       "muse-spark-1.3-contributor",
		configRowEffort:      "automático",
		configRowAutoCompact: "automático",
		configRowPermission:  "auto",
		configRowVoice:       "desligado",
		configRowTheme:       "Sistema",
		configRowStatusline:  "padrão",
		configRowStyle:       "concise",
		configRowRecaps:      "ligado",
		configRowMCP:         "nenhum",
	}
	for value, want := range wantMeta {
		if got := findConfigRow(t, m, value).Meta; got != want {
			t.Errorf("row %q value = %q, want %q", value, got, want)
		}
	}
	if got := findConfigRow(t, m, configRowPermission).Detail; !strings.Contains(got, "Shift+Tab") {
		t.Errorf("permission row should hint Shift+Tab, got %q", got)
	}
}

// A busca do painel filtra por nome e por valor com o applyQuery existente.
func TestConfigPanelSearchFiltersByNameAndValue(t *testing.T) {
	m := openConfigPanel(t, configPanelTestModel())

	m.picker.query = "voz"
	m.picker.applyQuery()
	if len(m.picker.items) != 2 {
		t.Fatalf("search \"voz\" should match 2 rows, got %d: %v", len(m.picker.items), m.picker.items)
	}
	for _, item := range m.picker.items {
		if item.Value != configRowVoice && item.Value != configRowSTT {
			t.Fatalf("search \"voz\" matched unexpected row %q", item.Value)
		}
	}

	m.picker.query = "automático"
	m.picker.applyQuery()
	matched := map[string]bool{}
	for _, item := range m.picker.items {
		matched[item.Value] = true
	}
	if !matched[configRowEffort] || !matched[configRowAutoCompact] {
		t.Fatalf("search by value should match effort and autocompact rows, got %v", matched)
	}

	m.picker.query = "zzz-sem-igual"
	m.picker.applyQuery()
	if len(m.picker.items) != 0 {
		t.Fatalf("unmatched query should empty the panel, got %d rows", len(m.picker.items))
	}
}

// Enter em "Resumo ao voltar" alterna na hora e grava no config.
func TestConfigPanelToggleRecapsPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := configPanelTestModel()
	m.userConfigPath = path
	m = openConfigPanel(t, m)

	updated, cmd := m.chooseConfigRow(findConfigRow(t, m, configRowRecaps))
	if cmd != nil {
		t.Fatal("expected recaps toggle without a background command")
	}
	next := updated.(model)
	if next.recapsEnabled {
		t.Fatal("recaps should be off after Enter on the row")
	}
	if next.picker == nil || next.picker.kind != pickerConfig {
		t.Fatal("recaps toggle should keep the panel open")
	}
	if got := findConfigRow(t, next, configRowRecaps).Meta; got != "desligado" {
		t.Fatalf("recaps row value = %q, want desligado", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Preferences map[string]any `json:"preferences"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Preferences["recaps"] != false {
		t.Fatalf("config file should persist recaps:false, got %s", raw)
	}
}

// Enter em "Tema" fecha o painel e abre o seletor de tema que já existe.
func TestConfigPanelThemeRowOpensThemePicker(t *testing.T) {
	m := openConfigPanel(t, configPanelTestModel())

	updated, _ := m.chooseConfigRow(findConfigRow(t, m, configRowTheme))
	next := updated.(model)
	if next.picker == nil || next.picker.kind != pickerTheme {
		t.Fatalf("expected the theme picker, got %+v", next.picker)
	}
}

// Enter em "Modo de permissão" (só leitura) mostra o cartão existente.
func TestConfigPanelPermissionRowShowsCard(t *testing.T) {
	m := openConfigPanel(t, configPanelTestModel())

	updated, _ := m.chooseConfigRow(findConfigRow(t, m, configRowPermission))
	next := updated.(model)
	if next.picker != nil {
		t.Fatal("permission row should close the panel")
	}
	if got := transcriptText(next.transcript); !strings.Contains(got, "Permissões") {
		t.Fatalf("permission row should show the permissions card, got:\n%s", got)
	}
}

// O painel renderiza em 80 e 120 colunas sem linha maior que a largura.
func TestConfigPanelRenderWidths(t *testing.T) {
	for _, width := range []int{80, 120} {
		m := configPanelTestModel()
		m.width = width
		m.height = 30
		m.picker = m.newConfigPicker()
		got := plainRender(t, m.pickerOverlay(width))
		for _, want := range []string{"Configurações", "buscar >", "❯ ", "↑/↓", "Enter alterar", "Esc fechar"} {
			if !strings.Contains(got, want) {
				t.Fatalf("width %d missing %q:\n%s", width, want, got)
			}
		}
		for _, line := range strings.Split(got, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d overflow (%d cells): %q", width, lipgloss.Width(line), line)
			}
		}
	}
}

// O erro de argumento desconhecido sai como cartão e sugere abrir /config.
func TestConfigUnknownArgSuggestsPanel(t *testing.T) {
	m := configPanelTestModel()
	_, text := m.handleConfigCommand("bogus")
	if !strings.Contains(text, commandCardTranscriptPrefix) {
		t.Fatalf("unknown arg should render as a card, got %q", text)
	}
	for _, want := range []string{"Ajuste desconhecido", "uso: recaps on|off", "/config"} {
		if !strings.Contains(text, want) {
			t.Fatalf("unknown arg card missing %q:\n%s", want, text)
		}
	}
}
