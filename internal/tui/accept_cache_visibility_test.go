package tui

import (
	"context"
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// Testes de aceitação da visibilidade do cache (statusline e /usage). O uso
// chega pelo mesmo caminho da execução real (agentUsageMsg em Update); a hora
// de cada chamada vem do relógio do model (m.now).

const (
	acceptCacheModelID = "accept-cache-model"
	acceptCacheOtherID = "accept-cache-other"
	acceptCacheBranch  = "feature-x"
)

// Preços BRL por 1M de tokens. Os modelos só existem no catálogo ao vivo,
// então é a única fonte de preço.
func acceptCacheTUIModel(t *testing.T, cachedInputPrice string) model {
	t.Helper()
	acceptCacheHermeticTUI(t)
	m := newModel(context.Background(), Options{
		Cwd:             "/workspace/qg",
		ProviderName:    "qg",
		ModelName:       acceptCacheModelID,
		ProviderProfile: config.ProviderProfile{Name: "qg", CatalogID: "qg", Model: acceptCacheModelID},
		Provider:        &fakeProvider{},
		NewProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
			return &fakeProvider{}, nil
		},
	})
	window := int64(1_000_000)
	pricing := qgaccount.ModelPricing{Currency: "BRL", Input: "1.648", Output: "4.9955", CachedInput: cachedInputPrice}
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{"qg": {
		{ID: acceptCacheModelID, Source: "live", APIModel: &qgaccount.ModelInfo{ID: acceptCacheModelID, ContextLength: &window, PricingBRL: pricing}},
		{ID: acceptCacheOtherID, Source: "live", APIModel: &qgaccount.ModelInfo{ID: acceptCacheOtherID, ContextLength: &window, PricingBRL: pricing}},
	}}
	m.gitBranch = acceptCacheBranch
	return m
}

func acceptCacheCall(m model, at time.Time, modelID string, input, cached, output int) model {
	m.now = func() time.Time { return at }
	updated, _ := m.Update(agentUsageMsg{runID: m.activeRunID, modelID: modelID, usage: qgruntime.Usage{
		ModelID: modelID, InputTokens: input, CachedInputTokens: cached, OutputTokens: output, Elapsed: time.Second,
	}})
	return updated.(model)
}

func acceptCacheTrueColor(t *testing.T) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	old := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = old })
}

var acceptSGR = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// acceptFG devolve "R;G;B" da cor de primeiro plano ativa no início de token
// dentro da linha crua, ou "" se o token não tiver cor.
func acceptFG(raw, token string) string {
	at := strings.Index(raw, token)
	if at < 0 {
		return ""
	}
	fg := ""
	for _, match := range acceptSGR.FindAllStringSubmatchIndex(raw[:at], -1) {
		params := raw[match[2]:match[3]]
		if params == "" || params == "0" || params == "39" {
			fg = ""
			continue
		}
		if i := strings.Index(params, "38;2;"); i >= 0 {
			parts := strings.Split(params[i+5:], ";")
			if len(parts) >= 3 {
				fg = strings.Join(parts[:3], ";")
			}
		}
	}
	return fg
}

func acceptRGB(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("%d;%d;%d", r>>8, g>>8, b>>8)
}

// Cores esperadas, aceitando o equivalente do tema quando existe (o qgTheme
// vence quando tem cor equivalente).
func acceptGreen() []string { return []string{"123;216;143", acceptRGB(qgTheme.green.GetForeground())} }
func acceptAmber() []string { return []string{"242;193;78", acceptRGB(qgTheme.amber.GetForeground())} }
func acceptGray() []string {
	return []string{"139;142;149", acceptRGB(qgTheme.muted.GetForeground()), acceptRGB(qgTheme.faint.GetForeground()), acceptRGB(qgTheme.faintest.GetForeground())}
}

func acceptIn(value string, set []string) bool {
	for _, candidate := range set {
		if value != "" && value == candidate {
			return true
		}
	}
	return false
}

var acceptReasons = []string{"troca de modelo", "conversa compactada", "conversa limpa", "pausa longa", "prefixo mudou", "motivo desconhecido"}

func acceptAnyReason(status string) string {
	for _, reason := range acceptReasons {
		if strings.Contains(status, reason) {
			return reason
		}
	}
	return ""
}

var acceptT0 = time.Date(2026, 11, 11, 14, 0, 0, 0, time.Local)

// Statusline: antes da 1ª chamada com uso, `cache —` em cinza.
func TestAcceptCacheStatusDashBeforeFirstUsage(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	acceptCacheTrueColor(t)
	raw := m.statusLine(160)
	if !strings.Contains(plainRender(t, raw), "cache —") {
		t.Fatalf("antes da 1ª chamada a statusline deve mostrar `cache —`; veio %q", plainRender(t, raw))
	}
	if fg := acceptFG(raw, "—"); !acceptIn(fg, acceptGray()) {
		t.Fatalf("`cache —` deve ser cinza (#8B8E95 ou cinza do tema); cor = %q", fg)
	}
}

// Hit da sessão = soma(cache lido) / soma(entrada), ponderado por
// tokens, desde o início da sessão. 97.000/100.000 = 97%. A última chamada
// sozinha daria 99% e a média simples por chamada, 49%.
func TestAcceptCacheStatusSessionHitIsTokenWeighted(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 2_000, 0, 10)
	m = acceptCacheCall(m, acceptT0.Add(30*time.Second), acceptCacheModelID, 98_000, 97_000, 10)
	status := plainRender(t, m.statusLine(160))
	if !strings.Contains(status, "cache 97%") {
		t.Fatalf("hit da sessão ponderado por tokens = 97%%; statusline = %q", status)
	}
}

// Faixas: ≥95% verde (#7BD88F), 80–94% amarelo (#F2C14E), <80% laranja
// (#F29B4E). Uma única chamada: é a 1ª da conversa, então nem a de 79% ganha
// motivo.
func TestAcceptCacheStatusBands(t *testing.T) {
	cases := []struct {
		name   string
		cached int
		label  string
		band   string
	}{
		{"97 verde", 97_000, "97%", "verde"},
		{"95 exato verde", 95_000, "95%", "verde"},
		{"94 amarelo", 94_000, "94%", "amarelo"},
		{"80 exato amarelo", 80_000, "80%", "amarelo"},
		{"79 laranja", 79_000, "79%", "laranja"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := acceptCacheTUIModel(t, "0.16995")
			acceptCacheTrueColor(t)
			m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 100_000, c.cached, 10)
			raw := m.statusLine(160)
			status := plainRender(t, raw)
			if !strings.Contains(status, "cache "+c.label) {
				t.Fatalf("statusline deve mostrar `cache %s`; veio %q", c.label, status)
			}
			fg := acceptFG(raw, c.label)
			switch c.band {
			case "verde":
				if !acceptIn(fg, acceptGreen()) {
					t.Fatalf("%s deve ser verde; cor = %q", c.label, fg)
				}
			case "amarelo":
				if !acceptIn(fg, acceptAmber()) {
					t.Fatalf("%s deve ser amarelo; cor = %q", c.label, fg)
				}
			case "laranja":
				if fg == "" || acceptIn(fg, acceptGreen()) || acceptIn(fg, acceptAmber()) || acceptIn(fg, acceptGray()) {
					t.Fatalf("%s deve ser laranja (#F29B4E); cor = %q", c.label, fg)
				}
			}
			if reason := acceptAnyReason(status); reason != "" {
				t.Fatalf("a 1ª chamada da conversa não ganha motivo; veio %q", reason)
			}
		})
	}
}

// Lado direito, depois do contexto e do branch, como em
// `● automático · alto │ dev │ 34% · 340k/1M │ cache 97%`.
func TestAcceptCacheStatusSitsAfterContext(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 340_000, 329_800, 10)
	status := plainRender(t, m.statusLine(180))
	cacheAt := strings.Index(status, "cache 97%")
	if cacheAt < 0 {
		t.Fatalf("statusline deve mostrar `cache 97%%`; veio %q", status)
	}
	contextAt := strings.Index(status, "34%")
	if contextAt < 0 || contextAt > cacheAt {
		t.Fatalf("o cache vem depois do contexto (34%%); statusline = %q", status)
	}
	if branchAt := strings.Index(status, acceptCacheBranch); branchAt < 0 || branchAt > cacheAt {
		t.Fatalf("o cache fica no lado direito, depois do branch; statusline = %q", status)
	}
}

// O cache some primeiro quando estreito, antes do branch. Estreitando a
// largura, o cache nunca sobrevive ao branch nem ao contexto, e existe uma
// largura em que o branch ainda aparece e o cache já sumiu.
func TestAcceptCacheStatusDropsFirstWhenNarrow(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 340_000, 329_800, 10)
	wide := plainRender(t, m.statusLine(220))
	if !strings.Contains(wide, "cache 97%") || !strings.Contains(wide, acceptCacheBranch) || !strings.Contains(wide, "34%") {
		t.Fatalf("na largura 220 cabem contexto, branch e cache; veio %q", wide)
	}
	branchWithoutCache := false
	for width := 220; width >= 20; width-- {
		status := plainRender(t, m.statusLine(width))
		hasCache := strings.Contains(status, "cache ")
		hasBranch := strings.Contains(status, acceptCacheBranch)
		hasContext := strings.Contains(status, "34%")
		if hasCache && (!hasBranch || !hasContext) {
			t.Fatalf("na largura %d o cache sobreviveu ao branch/contexto: %q", width, status)
		}
		if hasBranch && !hasCache {
			branchWithoutCache = true
		}
	}
	if !branchWithoutCache {
		t.Fatal("deveria existir uma largura com branch visível e cache já escondido")
	}
}

// Queda (<80%, não é a 1ª chamada) acrescenta ` · <motivo>` em laranja
// até uma chamada voltar a ≥80%. Cada causa é produzida pela superfície real.
func TestAcceptCacheDropReasons(t *testing.T) {
	type cause func(t *testing.T, m model) (model, time.Time, string)
	after := acceptT0.Add(30 * time.Second)
	cases := []struct {
		reason string
		cause  cause
	}{
		{"troca de modelo", func(t *testing.T, m model) (model, time.Time, string) {
			next, _, ok, err := m.handleModelCommandScope(acceptCacheOtherID, false)
			if !ok || err != nil {
				t.Fatalf("troca de modelo falhou: ok=%v err=%v", ok, err)
			}
			return next, after.Add(time.Minute), acceptCacheOtherID
		}},
		{"conversa compactada", func(t *testing.T, m model) (model, time.Time, string) {
			m.compactInFlight = true
			updated, _ := m.Update(compactResultMsg{result: CompactResult{Compacted: true}})
			return updated.(model), after.Add(time.Minute), acceptCacheModelID
		}},
		{"pausa longa", func(t *testing.T, m model) (model, time.Time, string) {
			return m, after.Add(6 * time.Minute), acceptCacheModelID
		}},
		{"motivo desconhecido", func(t *testing.T, m model) (model, time.Time, string) {
			return m, after.Add(time.Minute), acceptCacheModelID
		}},
		// "> 5 min": exatamente 5 min ainda não é pausa longa.
		{"motivo desconhecido", func(t *testing.T, m model) (model, time.Time, string) {
			return m, after.Add(5 * time.Minute), acceptCacheModelID
		}},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%d_%s", i, c.reason), func(t *testing.T) {
			m := acceptCacheTUIModel(t, "0.16995")
			acceptCacheTrueColor(t)
			m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 10_000, 0, 10)
			m = acceptCacheCall(m, after, acceptCacheModelID, 100_000, 99_000, 10)
			m, at, modelID := c.cause(t, m)
			m = acceptCacheCall(m, at, modelID, 100_000, 40_000, 10)

			raw := m.statusLine(200)
			status := plainRender(t, raw)
			match := regexp.MustCompile(`cache (\d+%) · ` + regexp.QuoteMeta(c.reason)).FindStringSubmatch(status)
			if match == nil {
				t.Fatalf("esperava `cache NN%% · %s`; statusline = %q", c.reason, status)
			}
			if other := acceptAnyReason(strings.Replace(status, c.reason, "", 1)); other != "" {
				t.Fatalf("um motivo só; veio também %q em %q", other, status)
			}
			reasonFG := acceptFG(raw, c.reason)
			if reasonFG == "" || acceptIn(reasonFG, acceptGreen()) || acceptIn(reasonFG, acceptAmber()) || acceptIn(reasonFG, acceptGray()) {
				t.Fatalf("o motivo deve ser laranja; cor = %q", reasonFG)
			}

			m = acceptCacheCall(m, at.Add(30*time.Second), modelID, 100_000, 99_000, 10)
			if reason := acceptAnyReason(plainRender(t, m.statusLine(200))); reason != "" {
				t.Fatalf("o motivo some quando uma chamada volta a ≥80%%; ainda mostra %q", reason)
			}
		})
	}
}

// /clear abre conversa nova. A 1ª chamada depois dele não ganha
// motivo de queda e o hit da sessão segue acumulando:
// (0 + 99.000 + 40.000) / (10.000 + 100.000 + 100.000) = 66%.
func TestAcceptCacheClearKeepsSessionHitWithoutReason(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 10_000, 0, 10)
	m = acceptCacheCall(m, acceptT0.Add(30*time.Second), acceptCacheModelID, 100_000, 99_000, 10)
	m.input.SetValue("/clear")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	m = acceptCacheCall(m, acceptT0.Add(time.Minute), acceptCacheModelID, 100_000, 40_000, 10)
	status := plainRender(t, m.statusLine(200))
	if !strings.Contains(status, "cache 66%") {
		t.Fatalf("depois do /clear o hit da sessão segue acumulando (66%%); statusline = %q", status)
	}
	if reason := acceptAnyReason(status); reason != "" {
		t.Fatalf("a 1ª chamada depois do /clear não ganha motivo; veio %q", reason)
	}
}

func acceptOpenUsage(t *testing.T, m model) string {
	t.Helper()
	m.input.SetValue("/usage")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.picker == nil || m.picker.kind != pickerUsage {
		t.Fatal("/usage não abriu o painel")
	}
	return plainRender(t, m.usagePanelOverlay(140))
}

func acceptLineWith(panel string, parts ...string) bool {
	for _, line := range strings.Split(panel, "\n") {
		all := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// /usage, aba Sessão: bloco "Cache desta sessão" com % + `meta 95%`,
// tokens em pt-BR e economia pelo preço de entrada vs cache lido do catálogo.
// 83,52M de 87M = 96%. Economia = 83,52M × (1,648 − 0,16995)/1M = R$ 123,45.
// Sem cache = 87M × 1,648/1M = R$ 143,38 (saída zero: as duas leituras de
// "sem cache seria" coincidem).
func TestAcceptCacheUsageSessionBlock(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 1_000_000, 0, 0)
	m = acceptCacheCall(m, acceptT0.Add(time.Minute), acceptCacheModelID, 86_000_000, 83_520_000, 0)
	panel := acceptOpenUsage(t, m)
	for _, want := range []string{
		"Cache desta sessão",
		"83,5M de 87,0M tokens de entrada vieram do cache",
		"Economia estimada: ~R$ 123,45 (sem cache seria ~R$ 143,38)",
	} {
		if !strings.Contains(panel, want) {
			t.Fatalf("faltou %q no painel:\n%s", want, panel)
		}
	}
	if !acceptLineWith(panel, "96%", "meta 95%") {
		t.Fatalf("a linha do bloco deve trazer `96%%` e `meta 95%%`:\n%s", panel)
	}
}

// Se o catálogo não tiver preço de cache para o modelo, a linha de economia
// some (nunca inventar preço). O % não depende de preço e continua.
func TestAcceptCacheUsageOmitsSavingsWithoutCachePrice(t *testing.T) {
	m := acceptCacheTUIModel(t, "")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 1_000_000, 0, 0)
	m = acceptCacheCall(m, acceptT0.Add(time.Minute), acceptCacheModelID, 86_000_000, 83_520_000, 0)
	panel := acceptOpenUsage(t, m)
	if !strings.Contains(panel, "Cache desta sessão") || !acceptLineWith(panel, "96%", "meta 95%") {
		t.Fatalf("o bloco de cache deve aparecer mesmo sem preço de cache:\n%s", panel)
	}
	if strings.Contains(panel, "Economia") || strings.Contains(panel, "sem cache seria") {
		t.Fatalf("sem preço de cache no catálogo a linha de economia deve sumir:\n%s", panel)
	}
}

// "Quedas nesta sessão": HH:MM local, % daquela chamada, motivo; só
// chamadas <80% que não são a 1ª; no máximo as 5 mais recentes. A chamada de
// 82% (14:08) não entra.
func TestAcceptCacheUsageDropsListFiveMostRecent(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 100_000, 0, 10)
	for i := 1; i <= 7; i++ {
		m = acceptCacheCall(m, acceptT0.Add(time.Duration(i)*time.Minute), acceptCacheModelID, 100_000, (40+i)*1_000, 10)
	}
	m = acceptCacheCall(m, acceptT0.Add(8*time.Minute), acceptCacheModelID, 100_000, 82_000, 10)
	panel := acceptOpenUsage(t, m)
	if !strings.Contains(panel, "Quedas nesta sessão") {
		t.Fatalf("faltou a seção `Quedas nesta sessão`:\n%s", panel)
	}
	for i := 3; i <= 7; i++ {
		clock := acceptT0.Add(time.Duration(i) * time.Minute).Format("15:04")
		if !acceptLineWith(panel, clock, fmt.Sprintf("%d%%", 40+i), "motivo desconhecido") {
			t.Fatalf("faltou a queda `%s %d%% motivo desconhecido`:\n%s", clock, 40+i, panel)
		}
	}
	for _, gone := range []time.Time{acceptT0, acceptT0.Add(time.Minute), acceptT0.Add(2 * time.Minute), acceptT0.Add(8 * time.Minute)} {
		if strings.Contains(panel, gone.Format("15:04")) {
			t.Fatalf("%s não deveria estar nas quedas (1ª chamada, além das 5 mais recentes ou ≥80%%):\n%s", gone.Format("15:04"), panel)
		}
	}
}

// "Última chamada": `Entrada: 181k · cache lido: 176k (97%) · saída: 2,1k`
// (o custo só se já existir hoje, então não é travado aqui).
func TestAcceptCacheUsageLastCallLine(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 181_000, 176_000, 2_100)
	panel := acceptOpenUsage(t, m)
	if !strings.Contains(panel, "Entrada: 181k · cache lido: 176k (97%) · saída: 2,1k") {
		t.Fatalf("linha da última chamada fora do formato:\n%s", panel)
	}
}

// O painel mantém o que já mostrava antes. Guarda: verde no dev.
func TestAcceptCacheUsageKeepsTodaysPanel(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	m = acceptCacheCall(m, acceptT0, acceptCacheModelID, 181_000, 176_000, 2_100)
	panel := acceptOpenUsage(t, m)
	for _, want := range []string{"[Sessão]", "Conta", "Última chamada", "Modelo ativo: " + acceptCacheModelID, "Janela: 1.000.000 tokens", "Uso da janela:", "Esc fechar"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("o painel perdeu %q:\n%s", want, panel)
		}
	}
}

// O resume recompõe o hit a partir dos eventos salvos, se existirem; senão
// começa do zero. Eventos no formato que o TUI grava (EventUsage).
func TestAcceptCacheResumeRecomposesSessionHit(t *testing.T) {
	m := acceptCacheTUIModel(t, "0.16995")
	store := testSessionStore(t)
	withUsage, err := store.Create(sessions.CreateInput{SessionID: "accept-cache-resume"})
	if err != nil {
		t.Fatal(err)
	}
	appendTestEvent(t, store, withUsage.SessionID, sessions.EventUsage, map[string]any{"promptTokens": 2000, "completionTokens": 10, "cachedInputTokens": 0, "model": acceptCacheModelID})
	appendTestEvent(t, store, withUsage.SessionID, sessions.EventUsage, map[string]any{"promptTokens": 98000, "completionTokens": 10, "cachedInputTokens": 97000, "model": acceptCacheModelID})
	empty, err := store.Create(sessions.CreateInput{SessionID: "accept-cache-empty"})
	if err != nil {
		t.Fatal(err)
	}
	m.sessionStore = store

	resumed, notice := m.handleResumeCommand(withUsage.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	if status := plainRender(t, resumed.statusLine(200)); !strings.Contains(status, "cache 97%") {
		t.Fatalf("o resume recompõe o hit da sessão a partir dos eventos salvos (97%%); statusline = %q", status)
	}

	fresh, notice := resumed.handleResumeCommand(empty.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	if status := plainRender(t, fresh.statusLine(200)); !strings.Contains(status, "cache —") {
		t.Fatalf("sessão retomada sem eventos de uso começa do zero (`cache —`); statusline = %q", status)
	}
}
