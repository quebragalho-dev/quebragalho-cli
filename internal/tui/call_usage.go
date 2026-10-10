package tui

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

type callThroughput struct {
	outputTokens int64
	elapsed      time.Duration
}

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
	row := transcriptRow{kind: rowSystem, tool: "model-usage", id: fmt.Sprintf("usage_%d_%d", runID, ordinal), runID: runID, text: fmt.Sprintf("Chamada %d · %s", ordinal, modelID), meta: map[string]string{"model": modelID}}
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

func renderCallUsage(row transcriptRow, width int) string {
	cost := "custo não informado"
	if amount, ok := new(big.Rat).SetString(row.meta["amount"]); ok && amount.Sign() >= 0 {
		switch row.meta["currency"] {
		case "BRL":
			cost = "R$ " + strings.ReplaceAll(amount.FloatString(2), ".", ",")
		case "USD":
			cost = "US$ " + amount.FloatString(2)
		}
	}
	return renderSystemNote(strings.Join(wrapPlainText(row.text+" · "+cost, maxInt(1, width-2)), "\n"), width)
}

func (m model) statusReadout(left string, width int) string {
	context := "ctx não informado"
	if n := m.apiContextWindow(); n > 0 {
		context = "ctx " + strconv.Itoa(n)
	}
	if width < 58 && m.apiContextWindow() == 0 {
		context = "ctx n/d"
	}
	rate := "média — toks/s"
	if stats := m.callThroughput[m.modelName]; stats.elapsed > 0 {
		rate = fmt.Sprintf("média %.1f toks/s", float64(stats.outputTokens)/stats.elapsed.Seconds())
	}
	right := context
	if width >= 80 && lipgloss.Width(left)+lipgloss.Width(context)+lipgloss.Width(rate)+12 <= width {
		right = rate + " │ " + context
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

func throughputFromSessionEvents(events []sessions.Event) map[string]callThroughput {
	result := map[string]callThroughput{}
	for _, event := range events {
		if event.Type != sessions.EventUsage {
			continue
		}
		var call struct {
			Model        string        `json:"model"`
			OutputTokens int64         `json:"completionTokens"`
			Elapsed      time.Duration `json:"call_elapsed_ns"`
		}
		if json.Unmarshal(event.Payload, &call) != nil || call.Model == "" || call.OutputTokens < 0 || call.Elapsed <= 0 {
			continue
		}
		stats := result[call.Model]
		stats.outputTokens += call.OutputTokens
		stats.elapsed += call.Elapsed
		result[call.Model] = stats
	}
	return result
}
