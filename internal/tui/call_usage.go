package tui

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

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

func (m model) contextWindowDisplay() (int, string) {
	if n := m.apiContextWindow(); n > 0 {
		return n, ""
	}
	if m.providerProfile.CatalogID != "qg" {
		return 0, ""
	}
	n := 1_000_000
	if strings.EqualFold(strings.TrimSpace(m.modelName), "grok-4.7") {
		n = 500_000
	}
	return n, " (fallback/docs)"
}

func usageRate(event qgruntime.Usage) string {
	return usageRateText(event.EffectiveOutputTokens(), event.Elapsed)
}

// usageRateText renders tokens-per-second ("42 tok/s", "12.4k tok/s") for an
// output count over an elapsed time. "" when there is nothing to divide:
// zero elapsed, no output tokens, or a rounded rate of zero.
func usageRateText(outputTokens int, elapsed time.Duration) string {
	if elapsed <= 0 || outputTokens <= 0 {
		return ""
	}
	rate := int(float64(outputTokens) / max(elapsed.Seconds(), time.Second.Seconds()))
	if rate == 0 {
		return ""
	}
	return formatUsageTokens(rate) + " tok/s"
}

func callUsageRow(runID, ordinal int, modelID string, event qgruntime.Usage, info qgaccount.ModelInfo) transcriptRow {
	if event.ModelID != "" {
		modelID = event.ModelID
	}
	row := transcriptRow{kind: rowSystem, tool: "model-usage", id: fmt.Sprintf("usage_%d_%d", runID, ordinal), runID: runID, text: modelID, meta: map[string]string{"model": modelID}}
	if normalized, _, err := usage.Normalize(event); err == nil {
		row.meta["tokens"] = formatUsageTokens(normalized.TotalTokens)
	}
	if rate := usageRate(event); rate != "" {
		row.meta["rate"] = rate
	}
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
	label := modelID
	// A brighter rate would not survive the plain-text wrap below, so the
	// whole receipt stays muted like before; the speed still reads first.
	if rate := strings.TrimSpace(row.meta["rate"]); rate != "" {
		label += " · " + rate
	}
	return renderSystemNote(strings.Join(wrapPlainText(label+" · "+callCostText(row), maxInt(1, width-2)), "\n"), width)
}

func (m model) statusReadout(left string, width int) string {
	// Speed now lives on the composer's top border; the status line keeps
	// only the context readout.
	context := m.contextStatusText(width)
	// The cache segment rides right after the context readout and before the
	// cwd. It drops first when narrow: whenever it shows, the full branch
	// must fit too (a truncated branch with a live cache would claim room
	// the cache did not leave), and below the branch gate it never shows.
	branchFull := m.statusBranchFull()
	// Reserve widths are content-only and width-independent, so every budget
	// below is monotonic in width: narrowing only ever removes content, never
	// resurrects it. The rendered right side may be shorter (dropped cache,
	// short context); budgets always assume the full rendering.
	cacheSeg, _ := cacheStatusSegment(m.cacheStatusData())
	reserveRight := m.statusReserveRight(cacheSeg)
	reserveLeft := lipgloss.Width(left) + lipgloss.Width(branchFull)
	reserveAvail := width - 2 - reserveLeft - lipgloss.Width(reserveRight)
	// The branch gate still hides the branch below 58 cells; the reserve
	// keeps counting it so crossing the gate never frees notice/path room.
	showBranch := branchFull != "" && width >= 58
	right := context
	showCache := false
	if cacheSeg != "" && width >= 58 {
		// The cache yields first: it only shows when the full layout fits
		// alongside it — full branch, full cwd, full context and, when a
		// notice is pending, the full notice. Whatever shows with the cache
		// therefore shows whole, and narrowing only ever removes content.
		need := lipgloss.Width(left) + lipgloss.Width(branchFull) + lipgloss.Width(reserveRight)
		if path := strings.TrimSpace(m.cwd); path != "" {
			need += lipgloss.Width(qgTheme.line.Render(" │ ")) + lipgloss.Width(shortenPath(path))
		}
		if forms := m.updateNoticeForms(); len(forms) > 0 {
			need += lipgloss.Width("  ") + lipgloss.Width(forms[0])
		}
		if need+9 <= width {
			right = segJoin(context, cacheSeg)
			showCache = true
		}
	}
	if !showCache {
		// A hidden cache holds no room: release its reservation so the rest
		// stops yielding to a segment that is not on screen. Showing the
		// cache still requires the full layout, so this release can never
		// resurrect anything the full layout already ruled out.
		reserveAvail = width - 2 - reserveLeft - lipgloss.Width(m.statusReserveRight(""))
	}
	if lipgloss.Width(left)+lipgloss.Width(right)+9 > width {
		right = ""
	}
	if showBranch {
		budget := width - lipgloss.Width(left) - lipgloss.Width(right) - 12
		take := minInt(24, budget-2)
		if showCache {
			// The fit above already reserved a whole branch: render it whole.
			take = minInt(24, len([]rune(strings.TrimSpace(m.gitBranch))))
		}
		// Short branch names ("dev", "main") render whole with whatever the
		// budget reserved once the cache vouched for the full layout.
		if take >= 5 || (showCache && take > 0) {
			left += qgTheme.line.Render(" │ ") + qgTheme.muted.Render(" "+truncateDisplayWidth(strings.TrimSpace(m.gitBranch), take))
		}
	}
	if forms := m.updateNoticeForms(); forms != nil {
		if line, ok := m.joinStatusWithNotice(left, right, forms, width, reserveAvail, showCache); ok {
			return fitStyledLine(line, width)
		}
		// A notice that fits nowhere hides the path with it, so narrowing
		// never hands the path the room the notice just left. The right
		// side (context, cache) keeps its normal rules: it never belonged
		// to the notice, so it stays whenever it fits on its own.
		if right == "" {
			return fitStyledLine(left, width)
		}
		return fitStyledLine(joinHeaderLine(left, right, width), width)
	}
	left = m.appendStatusWorkspace(left, right, reserveAvail)
	if right == "" {
		return fitStyledLine(left, width)
	}
	return fitStyledLine(joinHeaderLine(left, right, width), width)
}

// segJoin renders two right-side segments with the status line separator,
// omitting it when the first side is empty.
func segJoin(first, second string) string {
	if first == "" {
		return second
	}
	return first + qgTheme.line.Render(" │ ") + second
}

func lastUsageFromSessionEvents(events []sessions.Event) (usage.Normalized, bool, transcriptRow) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessions.EventUsage {
			continue
		}
		modelID, eventUsage, _, ok := parseUsageEventUsage(event.Payload)
		if !ok {
			continue
		}
		var cost struct {
			Amount   string `json:"cost_exact"`
			Currency string `json:"cost_currency"`
		}
		_ = json.Unmarshal(event.Payload, &cost)
		normalized, _, err := usage.Normalize(eventUsage)
		if err == nil {
			return normalized, true, transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": modelID, "amount": cost.Amount, "currency": cost.Currency}}
		}
	}
	return usage.Normalized{}, false, transcriptRow{}
}

func (m model) statusEffort() string {
	if m.reasoningEffort != "" {
		return effortDisplayLabel(string(m.reasoningEffort))
	}
	for _, candidate := range m.discoveredModelsForActiveProvider() {
		if candidate.Source != "live" || !strings.EqualFold(candidate.ID, m.modelName) {
			continue
		}
		effort := modelregistry.ReasoningEffort(strings.TrimSpace(candidate.DefaultReasoningEffort))
		if effort != "" && modelregistry.ValidReasoningEffort(effort) {
			return effortDisplayLabel(string(effort)) + " (API)"
		}
	}
	return "n/d"
}

// countStreamEstimate accumulates streamed output runes for the live speed
// estimate, stamping the first output and marking the call live. Called from
// the TUI update loop, so no locking is needed.
func (m *model) countStreamEstimate(delta string) {
	if n := len([]rune(delta)); n > 0 {
		if m.streamEstimateStart.IsZero() {
			m.streamEstimateStart = m.now()
		}
		m.streamEstimateChars += n
		m.streamLive = true
	}
}

// muteStreamLive ends the live-output state: the call's usage arrived or the
// model stopped emitting (tool running). The estimate resets so tool time
// never dilutes the next call's count.
func (m *model) muteStreamLive() {
	m.streamLive = false
	m.streamEstimateChars = 0
	m.streamEstimateStart = time.Time{}
}

// liveEstimateRate guesses the current speed from streamed output (~4
// characters per token, the same heuristic as the output budgets) over the
// wall time since the first output. "" until ~1s of output has arrived.
func (m model) liveEstimateRate() string {
	if m.streamEstimateStart.IsZero() || m.streamEstimateChars <= 0 {
		return ""
	}
	elapsed := m.now().Sub(m.streamEstimateStart)
	if elapsed < time.Second {
		return ""
	}
	return usageRateText(m.streamEstimateChars/4, elapsed)
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
