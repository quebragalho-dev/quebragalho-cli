package tui

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

func TestCacheStatusSegmentEmpty(t *testing.T) {
	seg, width := cacheStatusSegment(cacheStatusData{})
	if stripANSI(seg) != "cache —" {
		t.Fatalf("segmento vazio = %q, want %q", stripANSI(seg), "cache —")
	}
	if width != 7 {
		t.Fatalf("largura = %d, want 7", width)
	}
}

func TestCacheStatusSegmentBands(t *testing.T) {
	for _, tc := range []struct {
		hit  float64
		want string
		band string
	}{
		{0.97, "cache 97%", "green"},
		{0.95, "cache 95%", "green"},
		{0.9496, "cache 95%", "green"},
		{0.94, "cache 94%", "amber"},
		{0.80, "cache 80%", "amber"},
		{0.7996, "cache 80%", "amber"},
		{0.79, "cache 79%", "orange"},
		{0.00, "cache 0%", "orange"},
	} {
		seg, _ := cacheStatusSegment(cacheStatusData{hasData: true, hit: tc.hit})
		if stripANSI(seg) != tc.want {
			t.Fatalf("hit %.2f: segmento = %q, want %q", tc.hit, stripANSI(seg), tc.want)
		}
		if band := cacheHitBand(tc.hit); band != tc.band {
			t.Fatalf("hit %.2f: banda = %q, want %q", tc.hit, band, tc.band)
		}
	}
}

func TestCacheStatusSegmentDropReason(t *testing.T) {
	seg, _ := cacheStatusSegment(cacheStatusData{hasData: true, hit: 0.61, showDrop: true, reason: "pausa longa"})
	if stripANSI(seg) != "cache 61% · pausa longa" {
		t.Fatalf("segmento = %q, want motivo de queda", stripANSI(seg))
	}
	seg, _ = cacheStatusSegment(cacheStatusData{hasData: true, hit: 0.61})
	if stripANSI(seg) != "cache 61%" {
		t.Fatalf("sem queda o motivo nao aparece: %q", stripANSI(seg))
	}
}

func TestFormatCacheCount(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{2100, "2,1k"},
		{181000, "181k"},
		{176000, "176k"},
		{87000000, "87,0M"},
		{84900000, "84,9M"},
	} {
		if got := formatCacheCount(tc.n); got != tc.want {
			t.Fatalf("formatCacheCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestFormatCacheBRL(t *testing.T) {
	for _, tc := range []struct {
		amount string
		want   string
	}{
		{"12.40", "R$ 12,40"},
		{"15.80", "R$ 15,80"},
		{"0.006", "R$ 0,006"},
		{"0.0001", "< R$ 0,01"},
		{"0", "R$ 0,00"},
	} {
		amount, ok := new(big.Rat).SetString(tc.amount)
		if !ok {
			t.Fatal("bad fixture")
		}
		if got := formatCacheBRL(amount); got != tc.want {
			t.Fatalf("formatCacheBRL(%s) = %q, want %q", tc.amount, got, tc.want)
		}
	}
}

func TestDecideCacheDropReason(t *testing.T) {
	full := cacheDropInputs{prevModel: "a", curModel: "a", prevTools: "t1", curTools: "t1", prevSystem: "s1", curSystem: "s1"}
	for _, tc := range []struct {
		name string
		in   cacheDropInputs
		want string
	}{
		{"troca de modelo", cacheDropInputs{prevModel: "a", curModel: "b", prevTools: "t1", curTools: "t1", prevSystem: "s1", curSystem: "s1"}, "troca de modelo"},
		{"compactacao antes da pausa", cacheDropInputs{prevModel: "a", curModel: "a", compacted: true, gapLong: true, prevTools: "t1", curTools: "t1", prevSystem: "s1", curSystem: "s1"}, "conversa compactada"},
		{"ferramentas mudaram", cacheDropInputs{prevModel: "a", curModel: "a", prevTools: "t1", curTools: "t2", prevSystem: "s1", curSystem: "s1"}, "ferramentas mudaram"},
		{"ferramentas e system mudaram vira prefixo", cacheDropInputs{prevModel: "a", curModel: "a", prevTools: "t1", curTools: "t2", prevSystem: "s1", curSystem: "s2"}, "prefixo mudou"},
		{"pausa longa", cacheDropInputs{prevModel: "a", curModel: "a", gapLong: true, prevTools: "t1", curTools: "t1", prevSystem: "s1", curSystem: "s1"}, "pausa longa"},
		{"prefixo mudou", cacheDropInputs{prevModel: "a", curModel: "a", prevTools: "t1", curTools: "t1", prevSystem: "s1", curSystem: "s2"}, "prefixo mudou"},
		{"prefixo desconhecido nao acusa bug", cacheDropInputs{prevModel: "a", curModel: "a", prevTools: "", curTools: "t2", prevSystem: "", curSystem: "s2"}, "motivo desconhecido"},
		{"sem causa conhecida", full, "motivo desconhecido"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideCacheDropReason(tc.in); got != tc.want {
				t.Fatalf("motivo = %q, want %q", got, tc.want)
			}
		})
	}
}

func recordCacheTestUsage(m model, input, cached int, modelID, _ string) model {
	receipt := transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": modelID}}
	updated, _ := m.recordUsageEvent(modelID, qgruntime.Usage{InputTokens: input, CachedInputTokens: cached, OutputTokens: 10}, receipt)
	return updated
}

func recordCacheTestUsageParts(m model, input, cached int, modelID, _, tools, system string) model {
	receipt := transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": modelID, "cache_prefix_tools": tools, "cache_prefix_system": system}}
	updated, _ := m.recordUsageEvent(modelID, qgruntime.Usage{InputTokens: input, CachedInputTokens: cached, OutputTokens: 10}, receipt)
	return updated
}

func TestCacheStatusDataFromTracker(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 1000, 970, "kimi-k3.1-pro", "h1", "t1", "s1")
	m = recordCacheTestUsageParts(m, 1000, 200, "kimi-k3.1-pro", "h2", "t1", "s2")
	data := m.cacheStatusData()
	if !data.hasData {
		t.Fatal("com registros o segmento tem dados")
	}
	if data.hit < 0.58 || data.hit > 0.59 {
		t.Fatalf("hit = %.3f, want ~0.585", data.hit)
	}
	if !data.showDrop || data.reason != "prefixo mudou" {
		t.Fatalf("queda = show %v motivo %q, want prefixo mudou", data.showDrop, data.reason)
	}
}

func TestCacheQuedasCapFive(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsage(m, 1000, 970, "kimi-k3.1-pro", "h1")
	for i := 0; i < 10; i++ {
		m = recordCacheTestUsage(m, 1000, 100, "kimi-k3.1-pro", "h1")
	}
	if len(m.cacheDrops) != 5 {
		t.Fatalf("quedas guardadas = %d, want 5", len(m.cacheDrops))
	}
}

// stripANSI remove sequencias de escape para assercao de texto puro.
func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			in = true
			i++
			continue
		}
		if in {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				in = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestFormatCacheLastCall(t *testing.T) {
	got := formatCacheLastCall(cacheNormalizedForTest(181000, 176000, 2100), "6/1000", "BRL")
	want := "Entrada: 181k · cache lido: 176k (97%) · saída: 2,1k · R$ 0,006"
	if got != want {
		t.Fatalf("ultima chamada = %q, want %q", got, want)
	}
	got = formatCacheLastCall(cacheNormalizedForTest(100, 0, 50), "", "")
	want = "Entrada: 100 · cache lido: 0 (0%) · saída: 50 · custo não informado"
	if got != want {
		t.Fatalf("sem custo o marcador aparece: %q, want %q", got, want)
	}
}

func TestCacheSessionLines(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsage(m, 1000, 1000, "kimi-k3.1-pro", "h1")
	m = recordCacheTestUsage(m, 1000, 940, "kimi-k3.1-pro", "h1")
	lines := m.cacheSessionLines()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Cache desta sessão", "97%", "meta 95%", "1,9k de 2k tokens de entrada vieram do cache"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bloco nao contem %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Economia estimada") {
		t.Fatalf("sem preco de cache no catalogo a economia e omitida:\n%s", joined)
	}
	if strings.Contains(joined, "Quedas nesta sessão") {
		t.Fatalf("sem quedas a secao nao aparece:\n%s", joined)
	}
}

func TestCacheSessionLinesDrops(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 1000, 1000, "kimi-k3.1-pro", "h1", "t1", "s1")
	m = recordCacheTestUsageParts(m, 1000, 100, "kimi-k3.1-pro", "h2", "t1", "s2")
	joined := strings.Join(m.cacheSessionLines(), "\n")
	for _, want := range []string{"Quedas nesta sessão", "10%", "prefixo mudou"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bloco de quedas nao contem %q:\n%s", want, joined)
		}
	}
}

func TestCacheSessionLinesEmpty(t *testing.T) {
	m := newModel(context.Background(), Options{})
	if lines := m.cacheSessionLines(); len(lines) != 0 {
		t.Fatalf("sem registros o bloco e omitido, got %q", lines)
	}
}

func cacheNormalizedForTest(input, cached, output int) usage.Normalized {
	return usage.Normalized{InputTokens: input, CachedInputTokens: cached, OutputTokens: output}
}

func TestCacheToolsChangedMotive(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 1000, 990, "kimi-k3.1-pro", "h1", "t1", "s1")
	m = recordCacheTestUsageParts(m, 1000, 100, "kimi-k3.1-pro", "h2", "t2", "s1")
	data := m.cacheStatusData()
	if !data.showDrop || data.reason != "ferramentas mudaram" {
		t.Fatalf("carga de tool deve dizer `ferramentas mudaram`; show=%v motivo=%q", data.showDrop, data.reason)
	}
}

func TestCacheZeroInputNeverDrops(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 1000, 990, "kimi-k3.1-pro", "h1", "t1", "s1")
	m = recordCacheTestUsage(m, 0, 0, "kimi-k3.1-pro", "h1")
	data := m.cacheStatusData()
	if data.showDrop {
		t.Fatalf("chamada com entrada zero nao vira queda; veio %q", data.reason)
	}
	if len(m.cacheDrops) != 0 {
		t.Fatalf("quedas com entrada zero = %d, want 0", len(m.cacheDrops))
	}
}

func TestCacheSummarizerUsageStaysOutOfStats(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 40000, 39000, "kimi-k3.1-pro", "h1", "t1", "s1")
	summarizer := qgruntime.Usage{ModelID: "zz-summary", InputTokens: 30000, OutputTokens: 500, Summarizer: true}
	receipt := transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": "zz-summary"}}
	updated, _ := m.recordUsageEvent("zz-summary", summarizer, receipt)
	m = updated
	m = recordCacheTestUsageParts(m, 9000, 300, "kimi-k3.1-pro", "h2", "t1", "s1")
	if len(m.cacheCalls) != 2 {
		t.Fatalf("chamadas do sumarizador nao entram no hit; calls = %d, want 2", len(m.cacheCalls))
	}
	data := m.cacheStatusData()
	if !data.hasData || data.hit < 0.78 || data.hit > 0.82 {
		t.Fatalf("hit deve ignorar o sumarizador (~80%%); veio %.3f", data.hit)
	}
	if m.lastCacheModel != "kimi-k3.1-pro" {
		t.Fatalf("sumarizador nao pode trocar o ultimo modelo; veio %q", m.lastCacheModel)
	}
	if len(m.cacheDrops) != 1 || m.cacheDrops[0].reason != "motivo desconhecido" {
		t.Fatalf("queda esperada: 1x desconhecido (sem causa); veio %+v", m.cacheDrops)
	}
}

func TestCacheCompactionFlagMotive(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = recordCacheTestUsageParts(m, 40000, 39000, "kimi-k3.1-pro", "h1", "t1", "s1")
	receipt := transcriptRow{kind: rowSystem, tool: "model-usage", meta: map[string]string{"model": "kimi-k3.1-pro", "cache_prefix_tools": "t1", "cache_prefix_system": "s1", "cache_compacted": "1"}}
	updated, _ := m.recordUsageEvent("kimi-k3.1-pro", qgruntime.Usage{InputTokens: 9000, CachedInputTokens: 300, OutputTokens: 10}, receipt)
	m = updated
	data := m.cacheStatusData()
	if !data.showDrop || data.reason != "conversa compactada" {
		t.Fatalf("flag de compactacao deve motivar a queda; show=%v motivo=%q", data.showDrop, data.reason)
	}
}
