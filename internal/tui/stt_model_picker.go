package tui

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodelcatalog"
)

// sttModelValue encodes a picker selection as "provider:model" (model may be
// empty for the local engine, whose model is configured via stt.localModelPath).
const sttValueSep = ":"

// baselineSTTModels are the known transcription models per cloud provider (§7/§8).
// Used as a stable floor so the picker always offers sensible options even when
// the live catalog has not surfaced them (some providers omit transcription
// models or publish them without cost fields — see §8). Any additional
// IsSTTModel entries the catalog does surface are merged in on top.
var baselineSTTModels = map[string][]string{
	string(config.STTProviderGroq):   {"whisper-large-v3-turbo", "whisper-large-v3", "distil-whisper-large-v3-en"},
	string(config.STTProviderOpenAI): {"whisper-1", "gpt-4o-transcribe", "gpt-4o-mini-transcribe"},
}

// newSTTModelPicker builds the /stt-model overlay: a local-engine row plus the
// transcription models offered by Groq and OpenAI.
func (m model) newSTTModelPicker() *commandPicker {
	current := m.dictation.cfg
	var items []pickerItem

	// Local engine row — its model is a filesystem path set separately.
	localMeta := "motor offline sherpa-onnx"
	if current.STTProvider() == config.STTProviderLocal {
		localMeta = "atual · " + localMeta
	}
	items = append(items, pickerItem{
		Group: "Local",
		Label: "Local (sherpa-onnx)",
		Value: string(config.STTProviderLocal) + sttValueSep,
		Meta:  localMeta,
		// No locality dot: the Local/Groq/OpenAI section headers already say which is
		// which, so the marker was redundant.
	})

	for _, prov := range []string{string(config.STTProviderGroq), string(config.STTProviderOpenAI)} {
		group := titleCase(prov)
		for _, id := range sttModelsForProvider(prov) {
			meta := ""
			if current.STTProvider() == config.STTProviderKind(prov) && current.Model == id {
				meta = "atual"
			}
			items = append(items, pickerItem{
				Group: group,
				Label: id,
				Value: prov + sttValueSep + id,
				Meta:  meta,
			})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &commandPicker{
		kind:     pickerSTTModel,
		title:    "Escolher modelo de ditado (fala para texto)",
		items:    items,
		allItems: append([]pickerItem{}, items...),
	}
}

// sttModelsForProvider returns the transcription model ids for a cloud provider:
// the stable baseline, plus any additional IsSTTModel entries the live catalog
// surfaces, de-duplicated and baseline-first.
func sttModelsForProvider(provider string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range baselineSTTModels[provider] {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if descriptor, ok := providercatalog.Get(provider); ok {
		for _, model := range providermodelcatalog.Models(descriptor) {
			id := strings.TrimSpace(model.ID)
			if id == "" || seen[id] || !providermodelcatalog.IsSTTModel(model) {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (m model) openSTTModelPicker() (model, tea.Cmd) {
	picker := m.newSTTModelPicker()
	if picker == nil {
		return m, nil
	}
	m.picker = picker
	m.clearSuggestions()
	return m, nil
}

// maybeOpenSTTDownloadPicker opens the model-download chooser when the user
// picked the local engine, no model is configured yet, and this platform can
// auto-download. It persists the local-provider choice first so a later manual
// setup is still honored. Returns (model, true) when it opened the picker.
func (m model) maybeOpenSTTDownloadPicker(value string) (model, tea.Cmd, bool) {
	provider, _, _ := strings.Cut(value, sttValueSep)
	if config.STTProviderKind(provider) != config.STTProviderLocal {
		return m, nil, false
	}
	if m.dictation.downloadRoot == "" || !dictation.AutoDownloadSupported() {
		return m, nil, false // manual/cloud only on this platform
	}
	// Always open the chooser when Local is picked — so you can select a model the
	// first time AND switch models later (even with one already configured).
	// Don't persist Provider=local here — applyEngineComponents (called on both
	// the download-completion and the already-installed fast path) is the sole
	// place that commits the provider/model. Persisting now would silently
	// strand the user on an unconfigured local provider if they press Esc
	// out of the picker.
	m.clearSuggestions()
	if len(m.dictation.browseVariants) > 0 {
		// Full list already fetched this session — show it straight away.
		m.picker = newSTTDownloadPickerFrom(m.dictation.browseVariants, false, m.dictation.downloadRoot, m.engineDownloaded(), m.dictation.cfg.LocalModelPath, m.dictation.lang)
		return m, nil, true
	}
	// Show a single loading state while the full list is fetched — no curated-then-
	// swap flicker. handleSTTModelsFetched fills it in (or falls back to the curated
	// shortlist if the fetch fails, so the picker still works offline).
	m.dictation.browseLoading = true
	m.picker = newSTTDownloadLoadingPicker()
	return m, fetchSTTModelsCmd(), true
}

// newSTTDownloadLoadingPicker is the placeholder chooser shown while the full
// model list is being fetched — an empty, loading-marked picker so the overlay
// renders a "fetching…" line and its footer, matching the /model loading UX.
func newSTTDownloadLoadingPicker() *commandPicker {
	return &commandPicker{
		kind:    pickerSTTDownload,
		title:   "Baixar um modelo de ditado local — carregando…",
		loading: true,
	}
}

// sttModelsFetchedMsg carries the full model list fetched from the release.
type sttModelsFetchedMsg struct {
	variants []dictation.ModelVariant
	err      error
}

// fetchSTTModelsCmd lists every downloadable transcription model in the release,
// off the UI goroutine (a GitHub API round-trip).
func fetchSTTModelsCmd() tea.Cmd {
	return func() tea.Msg {
		// A stalled GitHub response would otherwise hang the picker loading
		// state forever (http.DefaultClient has no timeout). 30s is enough
		// headroom for the release + assets round-trips on a slow link.
		client := &http.Client{Timeout: 30 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		variants, err := dictation.ListModels(ctx, client, "")
		return sttModelsFetchedMsg{variants: variants, err: err}
	}
}

// handleSTTModelsFetched fills the loading download picker with the fetched full
// model list (preserving any filter the user typed while it loaded). If the fetch
// failed and nothing is cached, it falls back to the curated shortlist so the
// picker still works offline.
func (m model) handleSTTModelsFetched(msg sttModelsFetchedMsg) model {
	m.dictation.browseLoading = false
	if msg.err == nil && len(msg.variants) > 0 {
		m.dictation.browseVariants = msg.variants
	}
	if m.picker == nil || m.picker.kind != pickerSTTDownload {
		return m // the chooser was already closed
	}
	variants := m.dictation.browseVariants
	if len(variants) == 0 {
		variants = dictation.ModelVariants() // fetch failed → offline curated fallback
	}
	// Preserve the query AND the highlighted row across the fill-in.
	query := m.picker.query
	selectedValue := ""
	if item, ok := m.picker.current(); ok {
		selectedValue = item.Value
	}
	m.picker = newSTTDownloadPickerFrom(variants, false, m.dictation.downloadRoot, m.engineDownloaded(), m.dictation.cfg.LocalModelPath, m.dictation.lang)
	m.picker.query = query
	m.picker.applyQuery()
	m.selectPickerValue(selectedValue)
	return m
}

// handleSTTModelSelection persists the chosen dictation provider+model and
// updates the live controller so the next recording uses it (§11a — a switch
// takes effect for the next recording, never mid-utterance).
func (m model) handleSTTModelSelection(value string) (model, string) {
	provider, modelID, _ := strings.Cut(value, sttValueSep)
	kind := config.STTProviderKind(provider)

	if kind == config.STTProviderLocal {
		m.dictation.cfg.Provider = kind
		// Leave localModelPath as-is; the local model is a path, not chosen here.
		if _, err := config.SetSTTProvider(m.userConfigPath, kind); err != nil {
			return m, "Não foi possível salvar o provedor de ditado: " + err.Error()
		}
		hint := "Ditado configurado para o motor local sherpa-onnx."
		if strings.TrimSpace(m.dictation.cfg.LocalModelPath) == "" {
			// Auto-download platforms never reach here (the download chooser opens
			// instead); this is the manual-setup path (e.g. Termux).
			hint += " Configure stt.localModelPath para uma pasta de modelo ou escolha um provedor de nuvem em /stt-model."
		} else {
			hint += " Rode /voice; " + voiceCaptureUsage + "."
		}
		return m, hint
	}

	// Cloud provider. Route through the key prompt so a missing OR invalid key can be
	// set/replaced here, proactively — not only after a failed dictation (a saved key
	// can be present but invalid, and we can't tell until it's used). When a key
	// already resolves the prompt is optional: Esc keeps it and just applies the model.
	// Crucially, the active provider/model are only switched on commit, so cancelling
	// leaves a previously-working provider (e.g. Groq) untouched.
	if m.dictation.saveKey == nil {
		// No credential store wired on this platform — just apply the model.
		m.dictation.cfg.Provider = kind
		m.dictation.cfg.Model = modelID
		if _, err := config.SetSTTModel(m.userConfigPath, kind, modelID); err != nil {
			return m, "Não foi possível salvar o modelo de ditado: " + err.Error()
		}
		return m, "Modelo de ditado configurado para " + titleCase(provider) + " " + modelID + "."
	}
	hasKey := m.dictation.keyAvailable != nil && m.dictation.keyAvailable(provider)
	return m.openSTTKeyPrompt(provider, value, hasKey), ""
}

// voiceSetupPickerOpen reports whether a voice-setup picker owns the overlay:
// the first-use panel, or the download chooser while the /voice auto-enable
// is armed. Other pickers (including a manually opened chooser) are false.
func (m model) voiceSetupPickerOpen() bool {
	if m.picker == nil || m.picker.kind != pickerSTTDownload {
		return false
	}
	return m.picker.voiceFirstUse || m.dictation.voiceAfterSetup
}

// First-use panel values: row 1 carries the default variant's ID (handled by
// the regular variant path); rows 2 and 3 are panel-only commands.
const (
	// voiceFirstUseChoose opens the full download chooser, keeping the
	// auto-enable armed.
	voiceFirstUseChoose = "voice:choose-model"
	// voiceFirstUseLater closes the panel; nothing happens.
	voiceFirstUseLater = "voice:not-now"
)

// voiceFirstUseHeader explains the panel above its rows.
const voiceFirstUseHeader = "Para ditar, o QG baixa uma vez o reconhecimento de fala. Ele roda no seu\ncomputador: sua voz não é enviada a lugar nenhum."

// newVoiceFirstUsePicker builds the /voice first-use panel: download the
// default model now, pick another model, or skip. It shares the download
// chooser kind so selection, filtering and dismissal behave the same; the
// voiceFirstUse flag tells the flows apart.
func newVoiceFirstUsePicker(v dictation.ModelVariant, downloadRoot string, engineHave bool) *commandPicker {
	row1Label := fmt.Sprintf("Baixar agora — %s (%d MB)", v.Label, v.Bytes>>20+engineHaveBytes(engineHave))
	if dictation.ModelDownloaded(downloadRoot, v.DirName) && engineHave {
		row1Label = fmt.Sprintf("Usar %s (já baixado)", v.Label)
	}
	items := []pickerItem{
		{Label: row1Label, Value: v.ID, Meta: v.Description},
		{Label: "Escolher outro modelo…", Value: voiceFirstUseChoose},
		{Label: "Agora não", Value: voiceFirstUseLater},
	}
	return &commandPicker{
		kind:          pickerSTTDownload,
		title:         "Ditado por voz",
		header:        voiceFirstUseHeader,
		voiceFirstUse: true,
		items:         items,
		allItems:      append([]pickerItem{}, items...),
	}
}

// engineHaveBytes drops the shared engine's size from a per-model total once
// it is on disk (the same total the chooser shows for that row).
func engineHaveBytes(engineHave bool) int64 {
	if engineHave {
		return 0
	}
	return engineDownloadBytes >> 20
}

// openVoiceFirstUsePanel opens the first-use panel for the default model, or
// the full chooser when no model covers the user's language. Either way the
// auto-enable arms: the voice turns on when a model is ready. An unknown
// language always takes the chooser: DefaultVariantFor("") returns the
// multilingual default, but coverage of an unknown language is unconfirmed,
// so the user picks.
func (m model) openVoiceFirstUsePanel() (model, tea.Cmd) {
	if lang := strings.TrimSpace(m.dictation.lang); lang != "" {
		if variant, ok := dictation.DefaultVariantFor(lang); ok {
			m.dictation.voiceAfterSetup = true
			m.picker = newVoiceFirstUsePicker(variant, m.dictation.downloadRoot, m.engineDownloaded())
			m.clearSuggestions()
			return m, nil
		}
	}
	m.dictation.voiceAfterSetup = true
	next, cmd, opened := m.maybeOpenSTTDownloadPicker(string(config.STTProviderLocal) + sttValueSep)
	if !opened {
		// Unreachable: canOfferDownload (the only caller) already verified the
		// download root and platform support.
		return m.appendSystemNotice("Não foi possível abrir a lista de modelos de ditado."), nil
	}
	next = next.showTransientNoticeInline("Escolha um modelo de ditado. O modo de voz liga sozinho quando ele estiver pronto.", transientNoticeInfo)
	return next, cmd
}

// engineDownloaded reports whether the shared engine is already on disk.
func (m model) engineDownloaded() bool {
	return dictation.EngineDownloaded(m.dictation.downloadRoot, m.dictation.cfg.EngineVersion)
}

// newSTTDownloadPickerFrom builds the chooser from any variant list as a flat,
// un-grouped list — the Live/Batch pipeline is shown as a right-hand tag on each
// row (leading the meta) rather than as section headers, which read as cluttered
// and could even repeat a header when installed models sorted to the top. loading
// appends a hint that more models are still being fetched. downloadRoot lets it
// mark already-installed models; engineHave drops the shared engine's size from
// the per-model total once it is on disk. lang puts the user's language first:
// recommended first, then LanguageRank ascending, then installed, then smaller
// first. Row rendering does not change.
func newSTTDownloadPickerFrom(variants []dictation.ModelVariant, loading bool, downloadRoot string, engineHave bool, currentPath string, lang string) *commandPicker {
	isCurrent := func(v dictation.ModelVariant) bool {
		// Substring match false-positives when one variant's DirName is a
		// substring of another's ("tiny" inside "tiny-en"). Compare the
		// last path segment only.
		return currentPath != "" && v.DirName != "" && filepath.Base(currentPath) == v.DirName
	}
	sorted := append([]dictation.ModelVariant(nil), variants...)
	installed := func(v dictation.ModelVariant) bool {
		return dictation.ModelDownloaded(downloadRoot, v.DirName)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		// Rank by quality, pipeline-agnostic: the curated "good" picks — live AND batch
		// alike — rise together to the top (the Live/Batch tag on each row tells them
		// apart, so there's no separate live section). Then the user's language
		// first, then already-installed, then smallest/fastest first.
		if a.Recommended != b.Recommended {
			return a.Recommended
		}
		if ra, rb := dictation.LanguageRank(a, lang), dictation.LanguageRank(b, lang); ra != rb {
			return ra < rb
		}
		if ia, ib := installed(a), installed(b); ia != ib {
			return ia
		}
		return a.Bytes < b.Bytes
	})
	var items []pickerItem
	for _, v := range sorted {
		label := v.Label
		if v.Recommended {
			label = "★ " + label
		}
		totalMB := v.Bytes >> 20
		if !engineHave {
			totalMB += engineDownloadBytes >> 20 // engine is downloaded once, shared by all models
		}
		// Meta reads: pipeline tag · [description] · size · [state].
		tag := "Em lote"
		if v.Streaming {
			tag = "Ao vivo"
		}
		parts := []string{tag}
		if v.Description != "" {
			parts = append(parts, v.Description)
		}
		parts = append(parts, fmt.Sprintf("%d MB", totalMB))
		switch {
		case isCurrent(v):
			parts = append(parts, "● atual")
		case installed(v):
			parts = append(parts, "✓ baixado") // selecting re-applies it, no re-download
		}
		items = append(items, pickerItem{
			Label: label,
			Value: v.ID,
			Meta:  strings.Join(parts, " · "),
			// No Local dot: every row here is a local model, so the marker only added
			// noise. The Live/Batch tag in the meta is the meaningful distinction.
		})
	}
	// The search line and footer already convey "type to filter" and "Esc", so the
	// title stays just the name.
	title := "Baixar um modelo de ditado local"
	if loading {
		title = "Baixar um modelo de ditado local — carregando a lista completa…"
	}
	return &commandPicker{
		kind:     pickerSTTDownload,
		title:    title,
		items:    items,
		allItems: append([]pickerItem{}, items...),
	}
}

// engineDownloadBytes is the approximate size of the shared sherpa-onnx engine
// bundle, added to each model's size for the total-download display.
const engineDownloadBytes = 23 << 20

// handleSTTDownloadSelection starts downloading the chosen model variant (engine
// + that model). It resolves the variant from the picker's current items so any
// browse-listed model (not just the curated shortlist) is downloadable. It also
// serves the first-use panel rows: "choose" opens the full chooser keeping the
// auto-enable armed, "later" closes the panel and clears it.
func (m model) handleSTTDownloadSelection(value string) (model, tea.Cmd) {
	switch value {
	case voiceFirstUseLater:
		m.dictation.voiceAfterSetup = false
		return m, nil
	case voiceFirstUseChoose:
		if next, cmd, opened := m.maybeOpenSTTDownloadPicker(string(config.STTProviderLocal) + sttValueSep); opened {
			return next, cmd
		}
		return m.appendSystemNotice("Não foi possível abrir a lista de modelos de ditado."), nil
	}
	if v, ok := sttVariantByID(m.sttDownloadVariants(), value); ok {
		return m.applyVoiceVariant(v)
	}
	// The first-use panel names a curated default the fetched full list may
	// not include: fall back to the curated shortlist.
	if v, ok := sttVariantByID(dictation.ModelVariants(), value); ok {
		return m.applyVoiceVariant(v)
	}
	return m, nil
}

// sttVariantByID finds a variant by ID.
func sttVariantByID(variants []dictation.ModelVariant, id string) (dictation.ModelVariant, bool) {
	for _, v := range variants {
		if v.ID == id {
			return v, true
		}
	}
	return dictation.ModelVariant{}, false
}

// applyVoiceVariant applies an already-downloaded model instantly or starts
// its download, sharing the chooser row's logic for the first-use panel.
func (m model) applyVoiceVariant(v dictation.ModelVariant) (model, tea.Cmd) {
	// Already on disk (engine + this model): apply instantly, no download UI.
	if m.engineDownloaded() && dictation.ModelDownloaded(m.dictation.downloadRoot, v.DirName) {
		return m.applyInstalledModel(v)
	}
	return m.startVariantDownload(v)
}

// applyInstalledModel resolves the paths of an already-downloaded model (a fast,
// network-free EnsureLocalEngine) and applies them, with no download animation.
func (m model) applyInstalledModel(v dictation.ModelVariant) (model, tea.Cmd) {
	comp, err := dictation.EnsureLocalEngine(context.Background(), dictation.DownloadOptions{
		DestRoot:          m.dictation.downloadRoot,
		EngineVersion:     m.dictation.cfg.EngineVersion,
		ModelAssetName:    v.AssetName,
		ModelPinnedDigest: v.Digest,
		ModelDirName:      v.DirName,
		ModelLabel:        v.Label,
	})
	if err != nil {
		return m.startVariantDownload(v) // fell out of cache somehow — download it
	}
	m, aerr := m.applyEngineComponents(comp)
	if aerr != nil {
		return m.appendSystemNotice("Não foi possível salvar a configuração: " + aerr.Error()), nil
	}
	if m.dictation.voiceAfterSetup {
		m.dictation.voiceAfterSetup = false
		return m.enableVoiceMode(v.Label + " pronto. ")
	}
	return m.showTransientNoticeInline(v.Label+" está pronto. Rode /voice para começar a ditar.", transientNoticeSuccess), nil
}

// sttDownloadVariants returns the variants currently offered — the full fetched
// list when available, else the curated shortlist.
func (m model) sttDownloadVariants() []dictation.ModelVariant {
	if len(m.dictation.browseVariants) > 0 {
		return m.dictation.browseVariants
	}
	return dictation.ModelVariants()
}

// titleCase capitalizes the first letter of a lowercase provider id for display
// ("groq" → "Groq"), avoiding the deprecated strings.Title.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
