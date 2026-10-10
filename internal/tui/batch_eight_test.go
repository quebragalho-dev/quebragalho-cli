package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestBatchEightEffortCopy(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].APIModel.Capabilities.Reasoning = true
	m.reasoningEffort = "max"
	m = batchSixOpen(m, "/effort")
	got := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "effort: max")
	assertNotContains(t, got, "preferência local")
	assertNotContains(t, got, "não confirmado")
}

func TestBatchEightModelPrices(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].APIModel.PricingBRL = qgaccount.ModelPricing{Currency: "BRL", Input: "6.1285", Output: "31.93", CachedInput: "0.7725"}
	m = batchSixOpen(m, "/model")
	got := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "entrada R$ 6,1285 · saída R$ 31,93 · cache R$ 0,7725")
	assertNotContains(t, got, "Níveis locais")
	assertNotContains(t, got, "não confirmado")
	m.modelPickerLiveByProvider["qg"][0].APIModel.PricingBRL.Input = ""
	m = batchSixOpen(m, "/model")
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "entrada n/d")
}

func TestBatchEightSessionWindow(t *testing.T) {
	m := batchSixOpen(batchThreeLiveModel(t), "/usage")
	got := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "Janela: 1.000.000 tokens")
	assertNotContains(t, got, "fallback/docs")
	n := int64(200000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &n
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "Janela: 200.000 tokens")
}

func TestBatchEightAccountColumns(t *testing.T) {
	m := batchSixOpen(batchThreeLiveModel(t), "/usage")
	m.picker.usageTab = 1
	m.picker.account = &usageAccountResult{usage: qgaccount.UsageReport{From: "2026-10-09", To: "2026-10-10", Scope: "user", Requests: 12, InputTokens: 120000, OutputTokens: 3000, CachedInputTokens: 2000}, balance: qgaccount.Balance{Currency: "BRL", Available: "12.34"}}
	got := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "Custo no período: indisponível")
	assertContains(t, got, "A API não detalha custo nem uso por modelo.")
	column := -1
	for _, pair := range [][2]string{{"Chamadas:", "12"}, {"Entrada:", "120.000"}, {"Saída:", "3.000"}, {"Cache lido (incluído):", "2.000"}, {"Total:", "123.000"}, {"Saldo disponível:", "R$ 12,34"}} {
		found := false
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, pair[0]) {
				at := strings.Index(line, pair[1])
				if at < 0 {
					t.Fatalf("missing value %s", line)
				}
				if at < strings.Index(line, pair[0])+len(pair[0])+2 {
					t.Fatalf("label and value need a gap: %s", line)
				}
				cells := lipgloss.Width(line[:at])
				if column >= 0 && cells != column {
					t.Fatalf("column %d want %d: %s", cells, column, line)
				}
				column = cells
				found = true
			}
		}
		if !found {
			t.Fatalf("missing row %s", pair[0])
		}
	}
}

func TestBatchEightOccupancy(t *testing.T) {
	m := batchThreeLiveModel(t)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 120000, OutputTokens: 500, CachedInputTokens: 20000}, transcriptRow{})
	m = batchSixOpen(m, "/usage")
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "Uso da janela: 12% (120.000 / 1.000.000)")
	for _, kind := range []string{"missing", "other-model", "zero", "no-window"} {
		t.Run(kind, func(t *testing.T) {
			next := m
			switch kind {
			case "missing":
				next.lastUsageSeen = false
			case "other-model":
				next.modelName = "other"
			case "zero":
				next.lastUsage.InputTokens = 0
			case "no-window":
				next.providerProfile.CatalogID = "other"
			}
			got := plainRender(t, next.pickerOverlay(120))
			assertNotContains(t, got, "Uso da janela:")
			assertNotContains(t, got, "Ocupação:")
			assertNotContains(t, got, "Entrada da última")
		})
	}
}

func TestBatchEightBarePlanMode(t *testing.T) {
	for _, prior := range []agent.PermissionMode{agent.PermissionModeAsk, agent.PermissionModeAuto} {
		t.Run(string(prior), func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.permissionMode = prior
			m = batchSixOpen(m, "/plan")
			if m.permissionMode != agent.PermissionModePlan {
				t.Fatalf("/plan mode=%s want plan", m.permissionMode)
			}
			m = batchSixOpen(m, "/plan")
			m = batchSixOpen(m, "/plan status")
			if m.permissionMode != agent.PermissionModePlan {
				t.Fatal("repeated plan/status exited plan")
			}
			m = batchSixOpen(m, "/plan off")
			if m.permissionMode != prior {
				t.Fatalf("restored=%s want %s", m.permissionMode, prior)
			}
			m.pending = true
			m = batchSixOpen(m, "/plan")
			if m.permissionMode != prior {
				t.Fatal("mode changed during active turn")
			}
		})
	}
}
