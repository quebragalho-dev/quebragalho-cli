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
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

// cacheDrop journals one session-cache miss: a non-first call whose cache
// read fell below 80% of its input, with the detected cause.
type cacheDrop struct {
	at     time.Time
	hit    float64
	reason string
}

// maxCacheDropsShown caps the /usage drops list (and the journal) at the most
// recent entries.
const maxCacheDropsShown = 5

// cacheMissThreshold is the per-call hit below which a call counts as a drop.
const cacheMissThreshold = 0.8

// cacheStaleGap is the silence after which a miss blames a cold server cache.
const cacheStaleGap = 5 * time.Minute

// cacheStatusData is the isolated input of the statusline cache segment, so
// the Prisma catalog (item 3) can plug its own gathering behind it.
type cacheStatusData struct {
	hasData  bool
	hit      float64
	showDrop bool
	reason   string
}

// cacheHitBand names the statusline color band for a session hit.
func cacheHitBand(hit float64) string {
	switch {
	case hit >= 0.95:
		return "green"
	case hit >= 0.80:
		return "amber"
	default:
		return "orange"
	}
}

// cacheOrange renders the below-80 band. qgTheme has no orange equivalent,
// so the spec's hex rides here; lipgloss degrades it under NO_COLOR/Ascii
// like every other styled segment.
var cacheOrange = lipgloss.NewStyle().Foreground(lipgloss.Color("#F29B4B"))

// cacheStatusSegment renders the statusline cache segment: "cache —" before
// the first priced call, else "cache 97%" in the band color, plus " · <motivo>"
// while the last call is an unexplained-by-recovery drop.
func cacheStatusSegment(data cacheStatusData) (string, int) {
	if !data.hasData {
		seg := qgTheme.muted.Render("cache —")
		return seg, lipgloss.Width(seg)
	}
	var seg string
	pct := fmt.Sprintf("cache %.0f%%", data.hit*100)
	switch cacheHitBand(data.hit) {
	case "green":
		seg = qgTheme.green.Render(pct)
	case "amber":
		seg = qgTheme.amber.Render(pct)
	default:
		seg = cacheOrange.Render(pct)
	}
	if data.showDrop && strings.TrimSpace(data.reason) != "" {
		seg += cacheOrange.Render(" · " + strings.TrimSpace(data.reason))
	}
	return seg, lipgloss.Width(seg)
}

// cacheCall is one session-cache observation: token counts plus the model
// that served it, priced or not. The hit math never depends on pricing.
type cacheCall struct {
	modelID string
	input   int
	cached  int
	output  int
}

// cacheStatusData gathers the segment input from the session call journal.
func (m model) cacheStatusData() cacheStatusData {
	if len(m.cacheCalls) == 0 {
		return cacheStatusData{}
	}
	var input, cached int
	for _, call := range m.cacheCalls {
		input += call.input
		cached += call.cached
	}
	if input <= 0 {
		return cacheStatusData{}
	}
	data := cacheStatusData{hasData: true, hit: float64(cached) / float64(input)}
	if last := m.cacheCalls[len(m.cacheCalls)-1]; last.input > 0 &&
		float64(last.cached)/float64(last.input) < cacheMissThreshold &&
		len(m.cacheDrops) > 0 {
		// The journal's tail belongs to this same last call: drops are only
		// appended for non-first sub-80 calls, in order.
		drop := m.cacheDrops[len(m.cacheDrops)-1]
		data.showDrop = true
		data.reason = drop.reason
	}
	return data
}

// cacheDropInputs names everything a drop explanation may depend on. Prefix
// hashes come from the agent's per-request fingerprint; empty means unknown
// (never a mismatch).
type cacheDropInputs struct {
	prevModel, curModel   string
	compacted             bool
	gapLong               bool
	prevPrefix, curPrefix string
	prevTools, curTools   string
	prevSystem, curSystem string
}

// decideCacheDropReason names a sub-80 non-first call's cause in spec priority
// order. Unknown state reports "motivo desconhecido", never "prefixo mudou":
// that one stays reserved for a proven fingerprint mismatch (a bug).
func decideCacheDropReason(in cacheDropInputs) string {
	toolsChanged := in.prevTools != "" && in.curTools != "" && in.prevTools != in.curTools
	systemChanged := in.prevSystem != "" && in.curSystem != "" && in.prevSystem != in.curSystem
	switch {
	case in.prevModel != "" && in.curModel != "" && in.prevModel != in.curModel:
		return "troca de modelo"
	case in.compacted:
		return "conversa compactada"
	case toolsChanged && !systemChanged:
		return "ferramentas mudaram"
	case in.gapLong:
		return "pausa longa"
	case systemChanged:
		return "prefixo mudou"
	default:
		return "motivo desconhecido"
	}
}

// trackCacheUsage records one priced or unpriced usage event in the session
// call journal and updates the drop journal and the previous-call memory.
// callsBefore is the journal length before this event; boundary reports the
// first call of a cleared conversation, which never gains a drop motive.
// receipt carries the agent's per-request cache attribution in its meta
// ("cache_prefix_hash", "cache_compacted").
func (m model) trackCacheUsage(normalized usage.Normalized, receipt transcriptRow, callsBefore int, boundary bool) model {
	modelID := strings.TrimSpace(receipt.meta["model"])
	m.cacheCalls = append(m.cacheCalls, cacheCall{
		modelID: modelID,
		input:   normalized.InputTokens,
		cached:  normalized.CachedInputTokens,
		output:  normalized.OutputTokens,
	})
	prefix := strings.TrimSpace(receipt.meta["cache_prefix_hash"])
	toolsHash := strings.TrimSpace(receipt.meta["cache_prefix_tools"])
	systemHash := strings.TrimSpace(receipt.meta["cache_prefix_system"])
	compacted := strings.TrimSpace(receipt.meta["cache_compacted"]) == "1" || m.cacheCompactedPending
	m.cacheCompactedPending = false
	hit := 0.0
	if normalized.InputTokens > 0 {
		hit = float64(normalized.CachedInputTokens) / float64(normalized.InputTokens)
	}
	now := m.now()
	if !boundary && callsBefore > 0 && normalized.InputTokens > 0 && hit < cacheMissThreshold {
		reason := decideCacheDropReason(cacheDropInputs{
			prevModel:  m.lastCacheModel,
			curModel:   modelID,
			compacted:  compacted,
			gapLong:    !m.lastCacheAt.IsZero() && now.Sub(m.lastCacheAt) > cacheStaleGap,
			prevPrefix: m.lastCachePrefix,
			curPrefix:  prefix,
			prevTools:  m.lastCacheTools,
			curTools:   toolsHash,
			prevSystem: m.lastCacheSystem,
			curSystem:  systemHash,
		})
		m.cacheDrops = append(m.cacheDrops, cacheDrop{at: now, hit: hit, reason: reason})
		if len(m.cacheDrops) > maxCacheDropsShown {
			m.cacheDrops = append([]cacheDrop(nil), m.cacheDrops[len(m.cacheDrops)-maxCacheDropsShown:]...)
		}
	}
	if prefix != "" {
		m.lastCachePrefix = prefix
	}
	if toolsHash != "" {
		m.lastCacheTools = toolsHash
	}
	if systemHash != "" {
		m.lastCacheSystem = systemHash
	}
	if modelID != "" {
		m.lastCacheModel = modelID
	}
	m.lastCacheAt = now
	return m
}

func (m model) seedUsageFromSessionEvents(events []sessions.Event) model {
	if m.usageTracker == nil {
		return m
	}
	m.usageTracker.Reset()
	m.unpricedRequests = 0
	m.unpricedTokens = 0
	// Only totals are recomposed: the drop journal needs live prefix data,
	// and an unknown pre-resume prefix must never read as a mismatch.
	m.cacheDrops = nil
	m.cacheCalls = nil
	m.lastCachePrefix = ""
	m.lastCacheTools = ""
	m.lastCacheSystem = ""
	m.lastCacheModel = ""
	m.lastCacheAt = time.Time{}
	m.cacheClearBoundary = false
	for _, event := range events {
		if event.Type != sessions.EventUsage {
			continue
		}
		modelID, eventUsage, ok := parseUsageEventUsage(event.Payload)
		if !ok {
			continue
		}
		normalized, runtimeUsage, err := usage.Normalize(eventUsage)
		if err != nil {
			continue
		}
		m.cacheCalls = append(m.cacheCalls, cacheCall{
			modelID: modelID,
			input:   normalized.InputTokens,
			cached:  normalized.CachedInputTokens,
			output:  normalized.OutputTokens,
		})
		// Recompute cost with the current catalog, like the live path: a
		// resumed session keeps its token totals; unknown models ride the
		// unpriced counters instead of a fabricated $0.00.
		record, err := m.usageTracker.Record(usage.RecordInput{
			ModelID: modelID,
			Usage:   runtimeUsage,
			Source:  "resume",
		})
		if err != nil || record.Cost.Currency == "" {
			m.unpricedRequests++
			m.unpricedTokens += normalized.TotalTokens
			continue
		}
		m.lastCacheModel = modelID
		if at, err := time.Parse(time.RFC3339, strings.TrimSpace(event.CreatedAt)); err == nil {
			m.lastCacheAt = at
		}
	}
	return m
}

// parseUsageEventUsage reads the token fields of a saved provider_usage event.
func parseUsageEventUsage(payload json.RawMessage) (string, qgruntime.Usage, bool) {
	var call struct {
		Input      *int   `json:"promptTokens"`
		Output     *int   `json:"completionTokens"`
		Cached     int    `json:"cachedInputTokens"`
		CacheWrite int    `json:"cacheWriteTokens"`
		Reasoning  int    `json:"reasoningTokens"`
		Model      string `json:"model"`
	}
	if json.Unmarshal(payload, &call) != nil || call.Input == nil || call.Output == nil {
		return "", qgruntime.Usage{}, false
	}
	return call.Model, qgruntime.Usage{
		InputTokens:       *call.Input,
		OutputTokens:      *call.Output,
		CachedInputTokens: call.Cached,
		CacheWriteTokens:  call.CacheWrite,
		ReasoningTokens:   call.Reasoning,
	}, true
}

// cacheSessionLines builds the "Cache desta sessão" panel block: a banded bar
// with the session hit and the 95% goal, the cached input totals, the
// estimated savings (only when every served model has a cached BRL rate),
// and the most recent drops. Nil when there is nothing to show yet.
func (m model) cacheSessionLines() []string {
	if len(m.cacheCalls) == 0 {
		return nil
	}
	var input, cached int
	for _, call := range m.cacheCalls {
		input += call.input
		cached += call.cached
	}
	if input <= 0 {
		return nil
	}
	hit := float64(cached) / float64(input)
	var band lipgloss.Style
	switch cacheHitBand(hit) {
	case "green":
		band = qgTheme.green
	case "amber":
		band = qgTheme.amber
	default:
		band = cacheOrange
	}
	filled := int(hit*20 + 0.5)
	if filled > 20 {
		filled = 20
	}
	if filled < 0 {
		filled = 0
	}
	lines := []string{
		"Cache desta sessão",
		band.Render(strings.Repeat("█", filled)+strings.Repeat("░", 20-filled)) +
			" " + band.Render(fmt.Sprintf("%.0f%%", hit*100)) +
			" " + qgTheme.faint.Render("· meta 95%"),
		formatCacheCount(cached) + " de " + formatCacheCount(input) + " tokens de entrada vieram do cache",
	}
	if economy, ok := m.cacheEconomy(); ok {
		lines = append(lines, economy)
	}
	if len(m.cacheDrops) > 0 {
		lines = append(lines, "Quedas nesta sessão")
		for i := len(m.cacheDrops) - 1; i >= 0; i-- {
			drop := m.cacheDrops[i]
			lines = append(lines, drop.at.Local().Format("15:04")+" · "+fmt.Sprintf("%.0f%%", drop.hit*100)+" · "+drop.reason)
		}
	}
	return lines
}

// cacheEconomy renders the estimated savings line from live BRL pricing, or
// reports false when any served model lacks a cached-input BRL rate (the spec
// forbids inventing prices).
func (m model) cacheEconomy() (string, bool) {
	if len(m.cacheCalls) == 0 {
		return "", false
	}
	actual, without := new(big.Rat), new(big.Rat)
	for _, call := range m.cacheCalls {
		pricing, ok := m.brlPricingForModel(call.modelID)
		if !ok {
			return "", false
		}
		billed, err := pricing.Cost(qgruntime.Usage{
			InputTokens:       call.input,
			OutputTokens:      call.output,
			CachedInputTokens: call.cached,
		})
		if err != nil {
			return "", false
		}
		full, err := pricing.Cost(qgruntime.Usage{
			InputTokens:  call.input,
			OutputTokens: call.output,
		})
		if err != nil {
			return "", false
		}
		actual.Add(actual, billed)
		without.Add(without, full)
	}
	saved := new(big.Rat).Sub(without, actual)
	return "Economia estimada: ~" + formatCacheBRL(saved) + " (sem cache seria ~" + formatCacheBRL(without) + ")", true
}

// brlPricingForModel finds the live BRL price table for a model id, requiring
// a published cached-input rate. False when the catalog has nothing usable.
func (m model) brlPricingForModel(modelID string) (qgaccount.ModelPricing, bool) {
	for _, candidate := range m.discoveredModelsForActiveProvider() {
		if candidate.Source == "live" && strings.EqualFold(candidate.ID, strings.TrimSpace(modelID)) && candidate.APIModel != nil {
			pricing := candidate.APIModel.PricingBRL
			if pricing.Currency == "BRL" && strings.TrimSpace(pricing.CachedInput) != "" {
				return pricing, true
			}
			return qgaccount.ModelPricing{}, false
		}
	}
	return qgaccount.ModelPricing{}, false
}

// formatCacheLastCall renders the spec's one-line last-call summary.
func formatCacheLastCall(u usage.Normalized, amount, currency string) string {
	line := "Entrada: " + formatCacheCount(u.InputTokens) +
		" · cache lido: " + formatCacheCount(u.CachedInputTokens)
	if u.InputTokens > 0 {
		line += fmt.Sprintf(" (%d%%)", int(float64(u.CachedInputTokens)*100/float64(u.InputTokens)+0.5))
	}
	line += " · saída: " + formatCacheCount(u.OutputTokens)
	if cost := cacheLastCallCost(amount, currency); cost != "" {
		line += " · " + cost
	} else {
		line += " · custo não informado"
	}
	return line
}

// cacheLastCallCost renders the last call's exact cost: BRL amounts keep full
// precision (R$ 0,006), other priced currencies reuse the panel's existing
// receipt format, and missing costs are omitted.
func cacheLastCallCost(amount, currency string) string {
	if rat, ok := new(big.Rat).SetString(strings.TrimSpace(amount)); ok && rat.Sign() >= 0 && strings.EqualFold(strings.TrimSpace(currency), "BRL") {
		return formatCacheBRL(rat)
	}
	if cost := callCostText(transcriptRow{meta: map[string]string{"amount": amount, "currency": currency}}); cost != "custo não informado" {
		return cost
	}
	return ""
}

// formatCacheCount renders token counts the pt-BR way the cache UI wants:
// 999, 2,1k, 181k, 84,9M. Thousands drop a zero decimal; millions always
// carry one (87,0M), matching the spec examples.
func formatCacheCount(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 1_000_000 {
		return strings.ReplaceAll(trimZeroTenths(float64(n)/1000), ".", ",") + "k"
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".", ",") + "M"
}

// trimZeroTenths renders one decimal, dropping ".0".
func trimZeroTenths(value float64) string {
	text := fmt.Sprintf("%.1f", value)
	return strings.TrimSuffix(text, ".0")
}

// formatCacheBRL renders a BRL amount: centavos normally, thousandths below
// one centavo (R$ 0,006) so tiny last-call costs stay exact.
func formatCacheBRL(amount *big.Rat) string {
	if amount == nil || amount.Sign() <= 0 {
		return "R$ 0,00"
	}
	if new(big.Rat).Mul(amount, big.NewRat(100, 1)).Cmp(big.NewRat(1, 1)) < 0 {
		// Below one centavo: thousandths, rounded half away from zero; dust
		// that rounds to zero keeps the old "< R$ 0,01" floor.
		milli := new(big.Rat).Mul(amount, big.NewRat(1000, 1))
		num, den := milli.Num(), milli.Denom()
		rounded := new(big.Int).Quo(new(big.Int).Add(new(big.Int).Mul(num, big.NewInt(2)), den), new(big.Int).Mul(den, big.NewInt(2)))
		if rounded.Sign() == 0 {
			return "< R$ 0,01"
		}
		return "R$ " + strings.ReplaceAll(new(big.Rat).SetFrac(rounded, big.NewInt(1000)).FloatString(3), ".", ",")
	}
	return "R$ " + strings.ReplaceAll(amount.FloatString(2), ".", ",")
}
