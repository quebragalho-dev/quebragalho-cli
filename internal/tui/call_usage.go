package tui

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

func (m model) apiModel(modelID string) *qgaccount.ModelInfo {
	for _, candidate := range m.discoveredModelsForActiveProvider() {
		if candidate.Source == "live" && strings.EqualFold(candidate.ID, strings.TrimSpace(modelID)) {
			return candidate.APIModel
		}
	}
	return nil
}

func (m model) apiContextWindow() int {
	info := m.apiModel(m.modelName)
	if info == nil || info.ContextLength == nil || *info.ContextLength <= 0 || *info.ContextLength > int64(^uint(0)>>1) {
		return 0
	}
	return int(*info.ContextLength)
}

func callUsageRow(runID, ordinal int, modelID string, event qgruntime.Usage, info qgaccount.ModelInfo) transcriptRow {
	if event.ModelID != "" {
		modelID = event.ModelID
	}
	row := transcriptRow{kind: rowSystem, tool: "model-usage", id: fmt.Sprintf("usage_%d_%d", runID, ordinal), runID: runID, text: modelID, meta: map[string]string{"model": modelID}}
	if info.ID != "" {
		for _, pricing := range []qgaccount.ModelPricing{info.PricingBRL, info.Pricing} {
			if amount, err := pricing.Cost(event); err == nil {
				row.meta["currency"] = pricing.Currency
				row.meta["amount"] = amount.RatString()
				break
			}
		}
	}
	return row
}

func callCostText(row transcriptRow) string {
	cost := "custo não informado"
	if amount, ok := new(big.Rat).SetString(row.meta["amount"]); ok && amount.Sign() >= 0 {
		switch row.meta["currency"] {
		case "BRL":
			cost = "R$ " + strings.ReplaceAll(amount.FloatString(2), ".", ",")
			if amount.Sign() > 0 && amount.Cmp(big.NewRat(1, 100)) < 0 {
				cost = "< R$ 0,01"
			}
		case "USD":
			cost = "US$ " + amount.FloatString(2)
			if amount.Sign() > 0 && amount.Cmp(big.NewRat(1, 100)) < 0 {
				cost = "< US$ 0.01"
			}
		}
	}
	return cost
}

func renderCallUsage(row transcriptRow, width int) string {
	modelID := displayValue(row.meta["model"], row.text)
	return renderSystemNote(strings.Join(wrapPlainText(modelID+" · "+callCostText(row), maxInt(1, width-2)), "\n"), width)
}

func (m model) statusReadout(left string, width int) string {
	context := "ctx não informado"
	if n := m.apiContextWindow(); n > 0 {
		context = "ctx " + formatUsageTokens(n)
	}
	if width < 58 && m.apiContextWindow() == 0 {
		context = "ctx n/d"
	}
	tokens := "última n/d tok"
	if m.lastUsageSeen {
		tokens = "última " + formatUsageTokens(m.lastUsage.TotalTokens) + " tok"
	}
	right := context
	if width >= 80 && lipgloss.Width(left)+lipgloss.Width(context)+lipgloss.Width(tokens)+12 <= width {
		right = tokens + " │ " + context
	}
	if lipgloss.Width(left)+lipgloss.Width(right)+9 > width {
		right = ""
	}
	if branch := strings.TrimSpace(m.gitBranch); branch != "" && width >= 58 {
		budget := width - lipgloss.Width(left) - lipgloss.Width(right) - 12
		if budget >= 5 {
			left += qgTheme.line.Render(" │ ") + qgTheme.muted.Render(" "+truncateDisplayWidth(branch, minInt(24, budget-2)))
		}
	}
	left = m.appendStatusWorkspace(left, right, width)
	if right == "" {
		return fitStyledLine(left, width)
	}
	return fitStyledLine(joinHeaderLine(left, qgTheme.faint.Render(right), width), width)
}

func lastUsageFromSessionEvents(events []sessions.Event) (usage.Normalized, bool, transcriptRow) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessions.EventUsage {
			continue
		}
		var call struct {
			Input      *int   `json:"promptTokens"`
			Output     *int   `json:"completionTokens"`
			Cached     int    `json:"cachedInputTokens"`
			CacheWrite int    `json:"cacheWriteTokens"`
			Reasoning  int    `json:"reasoningTokens"`
			Model      string `json:"model"`
			Amount     string `json:"cost_exact"`
			Currency   string `json:"cost_currency"`
		}
		if json.Unmarshal(event.Payload, &call) != nil || call.Input == nil || call.Output == nil {
			continue
		}
		normalized, _, err := usage.Normalize(qgruntime.Usage{InputTokens: *call.Input, OutputTokens: *call.Output, CachedInputTokens: call.Cached, CacheWriteTokens: call.CacheWrite, ReasoningTokens: call.Reasoning})
		if err == nil {
			return normalized, true, transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": call.Model, "amount": call.Amount, "currency": call.Currency}}
		}
	}
	return usage.Normalized{}, false, transcriptRow{}
}

func (m model) statusEffort() string {
	if m.reasoningEffort != "" {
		return "esf " + string(m.reasoningEffort)
	}
	for _, candidate := range m.discoveredModelsForActiveProvider() {
		if candidate.Source != "live" || !strings.EqualFold(candidate.ID, m.modelName) {
			continue
		}
		effort := modelregistry.ReasoningEffort(strings.TrimSpace(candidate.DefaultReasoningEffort))
		if effort != "" && modelregistry.ValidReasoningEffort(effort) {
			return "esf " + string(effort) + " (API)"
		}
	}
	return "esf n/d"
}

func formatUsageTokens(n int) string {
	return strings.ReplaceAll(formatTokenCount(n), ",", ".")
}

func (msg agentResponseMsg) usageReceiptAt(index int) transcriptRow {
	id := fmt.Sprintf("usage_%d_%d", msg.runID, index+1)
	for _, row := range msg.rows {
		if row.tool == "model-usage" && row.id == id {
			return row
		}
	}
	return transcriptRow{}
}

func (m model) callUsageText() string {
	fields := []commandField{}
	if m.lastUsageSeen {
		fields = append(fields, commandField{Key: "Modelo da chamada", Value: displayValue(m.lastUsageReceipt.meta["model"], "não informado")},
			commandField{Key: "Entrada", Value: formatUsageTokens(m.lastUsage.InputTokens)},
			commandField{Key: "Saída", Value: formatUsageTokens(m.lastUsage.OutputTokens)},
			commandField{Key: "Cache lido (incluído)", Value: formatUsageTokens(m.lastUsage.CachedInputTokens)},
			commandField{Key: "Total", Value: formatUsageTokens(m.lastUsage.TotalTokens)},
			commandField{Key: "Custo", Value: callCostText(m.lastUsageReceipt)})
	} else {
		fields = append(fields, commandField{Key: "Última chamada", Value: "não informada"})
	}
	window := "não informado pela API"
	if n := m.apiContextWindow(); n > 0 {
		window = formatUsageTokens(n) + " tokens"
	}
	fields = append(fields, commandField{Key: "Modelo ativo", Value: displayValue(m.modelName, "não informado")}, commandField{Key: "Janela do modelo ativo", Value: window})
	return renderCommandCardTranscript(commandCard{Title: "Uso", Sections: []commandCardSection{{Title: "Última chamada", Fields: fields}}})
}
