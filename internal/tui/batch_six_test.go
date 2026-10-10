package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func batchSixKey(m model, code rune, text string) model {
	next, _ := m.Update(tea.KeyPressMsg{Code: code, Text: text})
	return next.(model)
}
func batchSixOpen(m model, command string) model {
	next, _ := m.dispatchCommand(parseCommand(command))
	return next.(model)
}
func TestBatchSixEffortPanelDraftAndScope(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].APIModel.Capabilities.Reasoning = true
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	m = batchSixOpen(m, "/effort")
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"Faster", "Smarter", "xhigh", "max"} {
		assertContains(t, got, want)
	}
	m = batchSixKey(m, tea.KeyRight, "")
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "▲")
	if m.reasoningEffort != "" {
		t.Fatal("draft changed active effort")
	}
	m = batchSixKey(m, tea.KeyEsc, "")
	if m.picker != nil || m.reasoningEffort != "" {
		t.Fatal("cancel did not discard draft")
	}
	m = batchSixOpen(m, "/effort")
	m = batchSixKey(m, tea.KeyRight, "")
	m = batchSixKey(m, 's', "s")
	if m.picker != nil || m.reasoningEffort != "low" {
		t.Fatalf("session selection: %s", m.reasoningEffort)
	}
	if _, err := os.Stat(m.userConfigPath); !os.IsNotExist(err) {
		t.Fatal("session wrote defaults")
	}
	m = batchSixOpen(m, "/effort")
	m = batchSixKey(m, tea.KeyRight, "")
	m = batchSixKey(m, tea.KeyEnter, "")
	data, err := os.ReadFile(m.userConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "medium") {
		t.Fatalf("default not saved: %s", data)
	}
}
func TestBatchSixModelPanelLiveDetails(t *testing.T) {
	m := batchThreeLiveModel(t)
	info := m.modelPickerLiveByProvider["qg"][0].APIModel
	label := "Muse Spark"
	info.DisplayName = &label
	info.AcceptsBonus = true
	info.Capabilities.Reasoning = true
	m.picker = m.newModelPicker()
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"1.", "Muse Spark", m.modelName, "BRL", "R$ 1,648", "bônus", "reasoning", "effort", "sessão"} {
		assertContains(t, got, want)
	}
}
func TestBatchSixUsageModalTabsAndCancel(t *testing.T) {
	m := batchThreeLiveModel(t)
	n := len(m.transcript)
	m = batchSixOpen(m, "/usage")
	if m.picker == nil || len(m.transcript) != n {
		t.Fatal("usage must open modal without appending transcript")
	}
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"Sessão", "Conta", "Janela: 1.000.000 tokens"} {
		assertContains(t, got, want)
	}
	m = batchSixKey(m, tea.KeyTab, "")
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "Conta")
	if m.input.Value() != "" {
		t.Fatal("tab leaked into composer")
	}
	m = batchSixKey(m, tea.KeyEsc, "")
	if m.picker != nil {
		t.Fatal("Esc did not close usage")
	}
}

func TestBatchSixNoCatalogFallbackAndPasteIsolation(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider = nil
	m, _ = m.openModelPicker()
	if m.picker == nil || len(m.picker.items) != 0 {
		t.Fatal("QG must not invent API models from registry")
	}
	m = batchSixOpen(m, "/effort")
	next, _ := m.Update(tea.PasteMsg{Content: "low"})
	m = next.(model)
	if m.picker.query != "" {
		t.Fatal("paste filtered a slider")
	}
}

func TestBatchSixPanelsWidthsAndLiveRefresh(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.height = 40
	m.modelPickerLiveByProvider["qg"][0].APIModel.Capabilities.Reasoning = true
	for _, command := range []string{"/effort", "/model", "/usage"} {
		for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
			m.width = width
			m = batchSixOpen(m, command)
			rendered := plainRender(t, m.pickerOverlay(width))
			assertRenderedLineWidths(t, rendered, width)
			assertContains(t, rendered, "Esc")
			if strings.Contains(rendered, "NaN") {
				t.Fatal(rendered)
			}
		}
	}
	m = batchSixOpen(m, "/model")
	m = batchSixKey(m, tea.KeyRight, "")
	before := m.picker.effort
	m = m.applyModelPickerModelsDiscovered(modelPickerModelsDiscoveredMsg{providerID: "qg", models: m.modelPickerLiveByProvider["qg"]})
	if m.picker.effort != before {
		t.Fatal("discovery lost effort draft")
	}
}

func TestBatchSixModelScopeAndEffortRestart(t *testing.T) {
	for _, session := range []bool{true, false} {
		t.Run(strconv.FormatBool(session), func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.providerProfile.Name = "qg"
			m.providerProfile.ProviderKind = config.ProviderKindOpenAICompatible
			m.providerProfile.Model = m.modelName
			m.providerProfile.APIKey = "fixture"
			m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
			_, err := config.UpsertProvider(m.userConfigPath, m.providerProfile, true)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(m.userConfigPath)
			info := qgaccount.ModelInfo{ID: "another-live-model", Capabilities: qgaccount.ModelCapabilities{Reasoning: true}}
			m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{ID: info.ID, Source: "live", APIModel: &info})
			m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) { return &fakeProvider{}, nil }
			m = batchSixOpen(m, "/model")
			m.picker.selected = 1
			m = batchSixKey(m, tea.KeyRight, "")
			if session {
				m = batchSixKey(m, 's', "s")
			} else {
				m = batchSixKey(m, tea.KeyEnter, "")
			}
			if m.modelName != info.ID || m.reasoningEffort != "low" || m.picker != nil {
				t.Fatalf("selection failed %s %s", m.modelName, m.reasoningEffort)
			}
			after, _ := os.ReadFile(m.userConfigPath)
			if session && string(after) != string(before) {
				t.Fatal("session changed persisted settings")
			}
			if !session {
				var cfg config.FileConfig
				if err := json.Unmarshal(after, &cfg); err != nil {
					t.Fatal(err)
				}
				if cfg.Providers[0].Model != info.ID {
					t.Fatal("default model not saved")
				}
				restarted := newModel(context.Background(), Options{UserConfigPath: m.userConfigPath, ModelName: info.ID, ProviderName: "qg"})
				if restarted.reasoningEffort != "low" {
					t.Fatal("effort not loaded on restart")
				}
			}
		})
	}
}

func TestBatchSixAccountUsesAPIAndIgnoresStaleResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/v1/usage":
			if r.URL.Query().Get("scope") != "user" {
				t.Error("missing scope")
			}
			fmt.Fprint(w, `{"scope":"user","from":"2026-10-01","to":"2026-10-02","requests":2,"input_tokens":12000,"output_tokens":771,"cached_input_tokens":1000}`)
		case "/v1/balance":
			fmt.Fprint(w, `{"currency":"BRL","available":"42.50"}`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m := batchThreeLiveModel(t)
	m.providerProfile.APIKey = "fixture"
	m.providerProfile.BaseURL = server.URL + "/v1"
	m = batchSixOpen(m, "/usage")
	next, cmd := m.Update(testKey(tea.KeyTab))
	m = next.(model)
	if cmd == nil {
		t.Fatal("account tab did not fetch")
	}
	result := cmd()
	next, _ = m.Update(result)
	m = next.(model)
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"12.771", "R$ 42,50", "Custo no período: indisponível"} {
		assertContains(t, got, want)
	}
	m = batchSixKey(m, tea.KeyEsc, "")
	m = batchSixOpen(m, "/usage")
	next, _ = m.Update(result)
	m = next.(model)
	if m.picker.account != nil {
		t.Fatal("stale result replaced new panel")
	}
}

func TestBatchSixEffortFailureAndCancelPreserveDefaults(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].APIModel.Capabilities.Reasoning = true
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	if _, err := config.SetReasoningEffort(m.userConfigPath, m.effortPreferenceKey(), "high"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.userConfigPath)
	m = batchSixOpen(m, "/effort")
	m = batchSixKey(m, tea.KeyRight, "")
	m = batchSixKey(m, tea.KeyEsc, "")
	after, _ := os.ReadFile(m.userConfigPath)
	if string(before) != string(after) {
		t.Fatal("cancel wrote defaults")
	}
	m.userConfigPath = t.TempDir()
	m = batchSixOpen(m, "/effort")
	m = batchSixKey(m, tea.KeyRight, "")
	m = batchSixKey(m, tea.KeyEnter, "")
	assertContains(t, m.transientNotice.text, "falha ao salvar")
	assertNotContains(t, m.transientNotice.text, "Padrão salvo")
}

func TestBatchSixContextBarOnlyUsesSameModelAPIWindow(t *testing.T) {
	m := batchThreeLiveModel(t)
	m = batchSixOpen(m, "/usage")
	event := qgruntime.Usage{InputTokens: 1000, OutputTokens: 250}
	receipt := callUsageRow(1, 1, m.modelName, event, *m.apiModel(m.modelName))
	m, _ = m.recordUsageEvent(m.modelName, event, receipt)
	got := plainRender(t, m.pickerOverlay(120))
	assertNotContains(t, got, "fallback/docs")
	assertContains(t, got, "0,1%")
	window := int64(2000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
	got = plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "50%")
	m.lastUsageReceipt.meta["model"] = "previous-model"
	assertNotContains(t, plainRender(t, m.pickerOverlay(120)), "%")
}

func batchSixUsageText(m model) string {
	m.picker = &commandPicker{kind: pickerUsage}
	return m.usagePanelOverlay(120)
}

func TestBatchSixSearchCanReturnToSessionSelection(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.picker = m.newModelPicker()
	m = batchSixKey(m, '/', "/")
	m = batchSixKey(m, 'm', "m")
	m = batchSixKey(m, 's', "s")
	if m.picker == nil || m.picker.query != "ms" {
		t.Fatal("search applied while typing")
	}
	m = batchSixKey(m, tea.KeyTab, "")
	if m.picker.searching {
		t.Fatal("Tab must return to list without discarding search")
	}
}
func TestBatchSixSameModelRebuildFailureKeepsPanel(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.providerProfile.Name = "qg"
	m.providerProfile.ProviderKind = config.ProviderKindOpenAICompatible
	m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) {
		return nil, fmt.Errorf("fixture rebuild failure")
	}
	m.picker = m.newModelPicker()
	m = batchSixKey(m, tea.KeyEnter, "")
	if m.picker == nil {
		t.Fatal("failed same-model rebuild announced success and closed panel")
	}
	assertContains(t, m.transientNotice.text, "fixture rebuild failure")
}
