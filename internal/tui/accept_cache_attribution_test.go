package tui

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// acceptRoutingProvider responde o sumarizador da compactação (request sem
// tools) com um roteiro próprio e os requests principais em ordem.
type acceptRoutingProvider struct {
	main           [][]qgruntime.StreamEvent
	summary        []qgruntime.StreamEvent
	mainCalls      int
	summarizeCalls int
	requests       []qgruntime.CompletionRequest
}

func (p *acceptRoutingProvider) StreamCompletion(_ context.Context, req qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	p.requests = append(p.requests, req)
	var events []qgruntime.StreamEvent
	if len(req.Tools) == 0 && p.summary != nil {
		p.summarizeCalls++
		events = p.summary
	} else {
		events = p.main[min(p.mainCalls, len(p.main)-1)]
		p.mainCalls++
	}
	ch := make(chan qgruntime.StreamEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func acceptAttrUsage(modelID string, input, cached, output int) qgruntime.StreamEvent {
	return qgruntime.StreamEvent{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{
		ModelID: modelID, InputTokens: input, CachedInputTokens: cached, OutputTokens: output,
	}}
}

func acceptAttrToolCall(tool string, usage qgruntime.StreamEvent) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: tool},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		usage,
		{Type: qgruntime.StreamEventDone},
	}
}

func acceptAttrText(text string, usage qgruntime.StreamEvent) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{{Type: qgruntime.StreamEventText, Content: text}, usage, {Type: qgruntime.StreamEventDone}}
}

// acceptLoaderTool devolve o mesmo sinal de carga que o tool_search devolve.
type acceptLoaderTool struct{}

func (acceptLoaderTool) Name() string        { return "accept_loader" }
func (acceptLoaderTool) Description() string { return "loads a deferred tool" }
func (acceptLoaderTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (acceptLoaderTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (acceptLoaderTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "ok"}
}
func (acceptLoaderTool) RunWithOptions(context.Context, map[string]any, tools.RunOptions) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "ok", Meta: map[string]string{"load_tools": "mcp__srv__alpha"}}
}

// acceptDeferredTool se comporta como uma tool MCP: elegível a adiamento.
type acceptDeferredTool struct{ name string }

func (t acceptDeferredTool) Name() string        { return t.name }
func (t acceptDeferredTool) Description() string { return "deferred " + t.name }
func (acceptDeferredTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (acceptDeferredTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (acceptDeferredTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "ok"}
}
func (acceptDeferredTool) Deferred() bool { return true }

const acceptAttrModel = "accept-attr-model"

// acceptAttrLiveModel monta o TUI real com catálogo ao vivo precificado e a
// janela pedida, para a compactação do agente disparar de verdade.
func acceptAttrLiveModel(t *testing.T, p qgruntime.Provider, registry *tools.Registry, window int, agentOptions agent.Options, compactionModel string) model {
	t.Helper()
	acceptCacheHermeticTUI(t)
	m := newModel(context.Background(), Options{
		Cwd:             t.TempDir(),
		ProviderName:    "qg",
		ModelName:       acceptAttrModel,
		ProviderProfile: config.ProviderProfile{Name: "qg", CatalogID: "qg", Model: acceptAttrModel},
		Provider:        p,
		Registry:        registry,
		SessionStore:    testSessionStore(t),
		AgentOptions:    agentOptions,
		CompactionModel: compactionModel,
		NewProvider:     func(config.ProviderProfile) (qgruntime.Provider, error) { return p, nil },
	})
	contextLength := int64(window)
	pricing := qgaccount.ModelPricing{Currency: "BRL", Input: "1.648", Output: "4.9955", CachedInput: "0.1648"}
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{"qg": {{
		ID: acceptAttrModel, Source: "live", ContextWindow: window,
		APIModel: &qgaccount.ModelInfo{ID: acceptAttrModel, ContextLength: &contextLength, PricingBRL: pricing},
	}}}
	writer := m.snapshotWriter
	t.Cleanup(func() {
		if writer != nil {
			writer.Flush(10 * time.Second)
		}
	})
	return m
}

func acceptAttrStatus(t *testing.T, m model) string {
	t.Helper()
	return plainRender(t, m.statusLine(220))
}

// Decisão: tools do prefixo mudaram e o system não, como quando o modelo
// carrega uma tool adiada via tool_search, é causa conhecida e tem motivo
// próprio. "prefixo mudou" fica só para system mudado sem causa conhecida.
func TestAcceptCacheToolsChangedReason(t *testing.T) {
	for _, tc := range []struct {
		name      string
		firstTool string
		reason    string
	}{
		{"carga de tool adiada", "accept_loader", "ferramentas mudaram"},
		{"controle sem carga", "read_it", "motivo desconhecido"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(acceptLoaderTool{})
			registry.Register(continuationEchoTool{})
			registry.Register(acceptDeferredTool{name: "mcp__srv__alpha"})
			registry.Register(acceptDeferredTool{name: "mcp__srv__beta"})
			registry.Register(tools.NewToolSearchTool(registry))
			p := &acceptRoutingProvider{main: [][]qgruntime.StreamEvent{
				acceptAttrToolCall(tc.firstTool, acceptAttrUsage("", 10_000, 9_700, 50)),
				acceptAttrText("ok", acceptAttrUsage("", 10_400, 600, 20)),
			}}
			m := acceptAttrLiveModel(t, p, registry, 1_000_000, agent.Options{DeferThreshold: 2}, "")
			m = runTextTurn(t, m, "q1")
			if len(p.requests) != 2 || p.requests[0].Messages[0].Content != p.requests[1].Messages[0].Content {
				t.Fatalf("pré-condição: 2 requests com o mesmo system, veio %d", len(p.requests))
			}
			loaded := slices.ContainsFunc(p.requests[1].Tools, func(d qgruntime.ToolDefinition) bool { return d.Name == "mcp__srv__alpha" })
			if loaded != (tc.firstTool == "accept_loader") {
				t.Fatalf("pré-condição: tool carregada no 2º request = %v", loaded)
			}
			status := acceptAttrStatus(t, m)
			if !strings.Contains(status, "· "+tc.reason) || strings.Contains(status, "prefixo mudou") {
				t.Fatalf("a queda depois de %s deveria mostrar %q; statusline = %q", tc.name, tc.reason, status)
			}
		})
	}
}

// Decisão: a chamada do sumarizador da compactação (sem OnContext) fica fora
// das estatísticas de cache. Não gera queda, não entra no hit nem na
// economia e não muda o modelo nem o prefixo anteriores, então a queda da
// chamada seguinte é atribuída à compactação mesmo com compactionModel.
func TestAcceptCacheSummarizerOutsideStats(t *testing.T) {
	for _, tc := range []struct{ name, summaryModel string }{
		{"sumarizador no modelo principal", ""},
		{"compactionModel dedicado", "accept-attr-summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(continuationEchoTool{})
			big := strings.Repeat("x", 120_000)
			first := append([]qgruntime.StreamEvent{{Type: qgruntime.StreamEventText, Content: big}},
				acceptAttrToolCall("read_it", acceptAttrUsage("", 40_000, 39_000, 100))...)
			p := &acceptRoutingProvider{
				main:    [][]qgruntime.StreamEvent{first, acceptAttrText("feito", acceptAttrUsage("", 9_000, 300, 20))},
				summary: acceptAttrText("RESUMO", acceptAttrUsage(tc.summaryModel, 30_000, 0, 300)),
			}
			m := acceptAttrLiveModel(t, p, registry, 24_000, agent.Options{CompactionPreserveLast: 2}, tc.summaryModel)
			m = runTextTurn(t, m, strings.Repeat("y", 40_000))
			if p.summarizeCalls != 1 || p.mainCalls != 2 {
				t.Fatalf("pré-condição: a compactação deveria chamar o sumarizador 1 vez entre 2 chamadas principais (sumarizador %d, principais %d)", p.summarizeCalls, p.mainCalls)
			}
			status := acceptAttrStatus(t, m)
			if !strings.Contains(status, "cache 80%") || !strings.Contains(status, "· conversa compactada") {
				t.Fatalf("hit só das chamadas principais (39,3k de 49k = 80%%) e queda atribuída à compactação; statusline = %q", status)
			}
			panel := acceptSGR.ReplaceAllString(acceptOpenUsage(t, m), "")
			if !strings.Contains(panel, "39,3k de 49k tokens de entrada vieram do cache") {
				t.Fatalf("o bloco de cache deveria contar só as chamadas principais:\n%s", panel)
			}
			if !strings.Contains(panel, "sem cache seria ~R$ 0,08") {
				t.Fatalf("a economia deveria contar só as chamadas principais (sem cache ~R$ 0,08):\n%s", panel)
			}
			if acceptLineWith(panel, "· 0% ·") || strings.Contains(panel, "troca de modelo") {
				t.Fatalf("a chamada do sumarizador não pode virar queda:\n%s", panel)
			}
		})
	}
}

var acceptShownPercent = regexp.MustCompile(`cache (\d+)%`)

// A cor segue a porcentagem exibida: quem lê "cache 95%" vê verde e quem lê
// "cache 80%" vê âmbar, seja qual for a casa decimal escondida.
func TestAcceptCacheColorMatchesShownPercent(t *testing.T) {
	acceptCacheTrueColor(t)
	for _, cached := range []int{9_496, 7_996} {
		m := acceptCacheTUIModel(t, "0.16995")
		m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 10_000, cached, 10)
		raw := m.statusLine(180)
		match := acceptShownPercent.FindStringSubmatch(acceptSGR.ReplaceAllString(raw, ""))
		if match == nil {
			t.Fatalf("statusline sem porcentagem de cache: %q", raw)
		}
		shown, _ := strconv.Atoi(match[1])
		want, band := []string{"242;155;78"}, "laranja"
		switch {
		case shown >= 95:
			want, band = acceptGreen(), "verde"
		case shown >= 80:
			want, band = acceptAmber(), "âmbar"
		}
		if fg := acceptFG(raw, "cache"); !slices.Contains(want, fg) {
			t.Fatalf("hit %d/10000 mostra %d%% e deveria estar em %s; cor = %q", cached, shown, band, fg)
		}
	}
}

// Um evento de uso sem tokens de entrada não diz nada sobre o cache e não
// pode aparecer em "Quedas nesta sessão" como 0%.
func TestAcceptCacheZeroInputIsNotADrop(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 100_000, 97_000, 100)
	m = acceptCacheCall(m, acceptT0.Add(time.Minute), acceptCacheModelID, 0, 0, 500)
	panel := acceptSGR.ReplaceAllString(acceptOpenUsage(t, m), "")
	if acceptLineWith(panel, "14:01", "0%") {
		t.Fatalf("evento sem entrada virou queda:\n%s", panel)
	}
}

// Decisão: o cache é o primeiro a ceder. Enquanto ele aparece, nada mais
// perdeu forma (aviso de versão, cwd, branch, contexto), e estreitar nunca
// faz algo reaparecer. A checagem de reaparecimento para no corte do branch:
// abaixo dele o layout troca de forma e o espaço do branch já devolvia cwd e
// aviso antes desta mudança.
func TestAcceptCacheYieldsFirstAndNothingReappears(t *testing.T) {
	for _, withNotice := range []bool{true, false} {
		m := acceptCacheTUIModel(t, "0.16995")
		m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 340_000, 329_800, 10)
		parts := map[string]string{
			"cache":             "cache ",
			"cwd inteiro":       "/workspace/qg",
			"branch":            acceptCacheBranch,
			"contexto completo": "34% · 340k/1m",
		}
		if withNotice {
			m.updateAvailable = "0.0.11"
			parts["aviso de versão"] = "nova"
			parts["aviso completo"] = "nova versão 0.0.11 · reinicie para instalar"
		}
		gone := map[string]int{}
		for width := 220; width >= 20; width-- {
			status := plainRender(t, m.statusLine(width))
			has := map[string]bool{}
			for name, token := range parts {
				has[name] = strings.Contains(status, token)
				if width == 220 && !has[name] {
					t.Fatalf("pré-condição: na largura 220 tudo cabe, faltou %s: %q", name, status)
				}
			}
			if has["cache"] {
				for name := range parts {
					if !has[name] {
						t.Fatalf("na largura %d o cache ficou e %s cedeu: %q", width, name, status)
					}
				}
			}
			if !has["branch"] {
				break
			}
			for name := range parts {
				if has[name] && gone[name] > 0 {
					t.Fatalf("%s sumiu na largura %d e reapareceu na %d: %q", name, gone[name], width, status)
				}
				if !has[name] && gone[name] == 0 {
					gone[name] = width
				}
			}
		}
	}
}
