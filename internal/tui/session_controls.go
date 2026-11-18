package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

var responseStyles = []string{"balanced", "concise", "explanatory", "review"}

const tuiCompactionPreserveLast = 8
const compactStatusRowID = "compact/status"

var compactFrames = []string{"⠂", "⠒", "⠲", "⠴"}

type SessionCompactor interface {
	CompactSession(context.Context, CompactRequest) (CompactResult, error)
}

type CompactRequest struct {
	SessionID             string
	ModelName             string
	ContextWindow         int
	SessionEventCount     int
	EstimatedTokens       int
	VisibleTranscriptRows int
	CompactRequests       int
}

type CompactResult struct {
	Compacted    bool
	BeforeTokens int
	AfterTokens  int
	Summary      string
}

type compactResultMsg struct {
	result             CompactResult
	err                error
	activeSession      sessions.Metadata
	sessionEvents      []sessions.Event
	transcript         []transcriptRow
	hasSessionSnapshot bool
	// compactionUsage carries the summarizer provider call's usage back to
	// the Update loop (runCompact runs async and must not touch model
	// state). Empty when the provider reported no usage: then no usage
	// event is recorded.
	compactionUsage    qgruntime.Usage
	compactionModel    string
	hasCompactionUsage bool
}

func (m model) handleEffortCommand(args string) (model, string) {
	raw := strings.TrimSpace(args)
	normalized := strings.TrimSpace(strings.ToLower(canonicalEffortInput(raw)))
	if normalized == "" || normalized == "list" {
		return m, m.effortText()
	}
	if normalized == "auto" {
		m.reasoningEffort = ""
		return m, m.effortStatusCard("auto", "Auto é a preferência local: não envia reasoning_effort. O esforço aplicado pelo servidor não é informado.")
	}

	requested := modelregistry.ReasoningEffort(normalized)
	if !modelregistry.ValidReasoningEffort(requested) {
		return m, m.effortStatusCard(m.effortDisplay(), "Nível de raciocínio desconhecido: "+raw)
	}
	efforts := m.panelReasoningEfforts()
	if len(efforts) == 0 {
		return m, m.effortStatusCard("", "O modelo ativo não oferece controles de nível de raciocínio.")
	}
	if !reasoningEffortAllowed(efforts, requested) {
		return m, m.effortStatusCard(m.effortDisplay(),
			fmt.Sprintf("Nível de raciocínio %q não suportado por %s.", effortDisplayLabel(string(requested)), displayValue(m.modelName, "o modelo ativo")))
	}

	m.reasoningEffort = requested
	return m, m.effortStatusCard(string(requested), "Preferência de nível de raciocínio salva nesta sessão.")
}

func (m model) discoveredModelsForActiveProvider() []providermodeldiscovery.Model {
	for _, key := range []string{m.providerProfile.CatalogID, m.providerProfile.Name, m.providerName} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if models := m.modelPickerLiveByProvider[key]; len(models) > 0 {
			return models
		}
	}
	return nil
}

// effortStatusCard renders the small inline confirmation card shown after a
// /effort <value> mutation (set, auto, unknown, unsupported). The body is a
// lime-bordered card so the transition from "picker open" -> "card collapsed"
// reads as the same surface the picker came from, instead of a separate grey
// status block.
func (m model) effortStatusCard(value string, detail string) string {
	active := strings.TrimSpace(value)
	if active == "" {
		active = "auto"
	}
	fields := []commandField{
		{Key: "nível ativo", Value: effortDisplayLabel(active)},
		{Key: "modelo", Value: displayValue(m.modelName, "nenhum")},
	}
	return renderCommandCardTranscript(commandCard{
		Title:   "Nível",
		Summary: []string{"nível ativo: " + effortDisplayLabel(active)},
		Sections: []commandCardSection{{
			Title:  "Estado",
			Fields: fields,
			Lines:  []string{detail},
		}},
	})
}

func (m model) effortText() string {
	efforts := m.availableReasoningEfforts()
	fields := []commandField{
		{Key: "preferência local", Value: m.effortDisplay()},
		{Key: "auto", Value: "não envia reasoning_effort; default do servidor não confirmado"},
		{Key: "nível ativo", Value: effortDisplayLabel(m.effortDisplay())},
		{Key: "modelo", Value: displayValue(m.modelName, "nenhum")},
	}
	actions := []string{"use /effort <valor> para trocar", "/effort auto para limpar"}
	if len(efforts) == 0 {
		fields = append(fields, commandField{Key: "disponíveis", Value: "API não informa níveis disponíveis"})
		return renderCommandCardTranscript(commandCard{
			Title:    "Nível",
			Summary:  []string{"nível ativo: " + effortDisplayLabel(m.effortDisplay()), "API não informa níveis disponíveis"},
			Sections: []commandCardSection{{Title: "Estado", Fields: fields}},
			Actions:  actions,
		})
	}
	fields = append(fields, commandField{Key: "disponíveis", Value: joinReasoningEffortLabels(efforts)})
	levelsSummary := fmt.Sprintf("%d níveis suportados", len(efforts))
	if len(efforts) == 1 {
		levelsSummary = "1 nível suportado"
	}
	return renderCommandCardTranscript(commandCard{
		Title:    "Nível",
		Summary:  []string{"nível ativo: " + effortDisplayLabel(m.effortDisplay()), levelsSummary},
		Sections: []commandCardSection{{Title: "Estado", Fields: fields}},
		Actions:  actions,
	})
}

// joinReasoningEffortLabels renders supported effort values as a Portuguese
// display list; the raw values stay untouched for config and protocol.
func joinReasoningEffortLabels(efforts []modelregistry.ReasoningEffort) string {
	values := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		values = append(values, effortDisplayLabel(string(effort)))
	}
	return strings.Join(values, ", ")
}

// reconcileEffortForModelSwitch applies the effort rules for a model switch.
// efforts is the destination's supported ring and ringKnown whether that ring
// is authoritative (a catalog entry, whose ring may be legitimately empty) or
// missing knowledge (a live-discovered/custom model the catalog cannot vouch
// for either way).
//
// A KNOWN-unsupported preference is dropped — the TUI run path forwards the
// effort to the provider unfiltered, so it must not survive the switch. An
// UNKNOWN ring never drops an explicit preference: the model may well support
// it, matching the cross-provider picker's behavior for custom models. The
// second return reports whether an unsupported preference was dropped (the
// caller's "(unsupported preference reset)" display case).
func (m model) reconcileEffortForModelSwitch(efforts []modelregistry.ReasoningEffort, ringKnown bool) (model, bool) {
	dropped := false
	if ringKnown && m.reasoningEffort != "" && !reasoningEffortAllowed(efforts, m.reasoningEffort) {
		m.reasoningEffort = ""
		dropped = true
	}
	return m, dropped && m.reasoningEffort == ""
}

func (m model) availableReasoningEfforts() []modelregistry.ReasoningEffort {
	if strings.TrimSpace(m.modelName) == "" {
		return nil
	}
	if efforts, found := m.discoveredReasoningEfforts(); found {
		return efforts
	}
	registry, err := modelregistry.DefaultRegistry()
	if err != nil {
		return nil
	}
	return registry.ReasoningEfforts(m.modelName)
}

func (m model) discoveredReasoningEfforts() ([]modelregistry.ReasoningEffort, bool) {
	return reasoningEffortsFromModels(m.discoveredModelsForActiveProvider(), m.modelName)
}

func reasoningEffortsFromModels(models []providermodeldiscovery.Model, modelName string) ([]modelregistry.ReasoningEffort, bool) {
	for _, discovered := range models {
		if !strings.EqualFold(strings.TrimSpace(discovered.ID), strings.TrimSpace(modelName)) {
			continue
		}
		seen := map[modelregistry.ReasoningEffort]bool{}
		efforts := make([]modelregistry.ReasoningEffort, 0, len(discovered.ReasoningEfforts))
		for _, raw := range discovered.ReasoningEfforts {
			effort := modelregistry.ReasoningEffort(strings.ToLower(strings.TrimSpace(raw)))
			if effort == modelregistry.ReasoningEffortNone || !modelregistry.ValidReasoningEffort(effort) || seen[effort] {
				continue
			}
			seen[effort] = true
			efforts = append(efforts, effort)
		}
		return efforts, true
	}
	return nil, false
}

func (m model) effortDisplay() string {
	if m.reasoningEffort == "" {
		return "auto"
	}
	return string(m.reasoningEffort)
}

// cycleReasoningEffort advances the reasoning-effort ring:
// auto ("") -> first supported -> ... -> last supported -> auto. No-op (model
// unchanged) when the active model exposes no effort controls, so Ctrl+T stays
// quiet on non-reasoning models. Called from the Ctrl+T key case — a rare
// keypress — so the DefaultRegistry() lookup inside availableReasoningEfforts
// is fine here, but MUST NOT be called from the render path (the registry is
// rebuilt on every call).
func (m model) cycleReasoningEffort() (model, tea.Cmd) {
	efforts := m.availableReasoningEfforts()
	if len(efforts) == 0 {
		return m, nil
	}
	// Every branch below changes the effort; a cycle is an explicit user
	// choice for this session.
	if m.reasoningEffort == "" {
		m.reasoningEffort = efforts[0]
		return m, nil
	}
	idx := reasoningEffortIndex(efforts, m.reasoningEffort)
	if idx == -1 || idx == len(efforts)-1 {
		m.reasoningEffort = "" // wrap to auto
		return m, nil
	}
	m.reasoningEffort = efforts[idx+1]
	return m, nil
}

func reasoningEffortAllowed(efforts []modelregistry.ReasoningEffort, want modelregistry.ReasoningEffort) bool {
	for _, effort := range efforts {
		if effort == want {
			return true
		}
	}
	return false
}

// reasoningEffortIndex returns the position of want in efforts, or -1. Sibling
// to reasoningEffortAllowed; used by cycleReasoningEffort to find the current
// slot in the model's supported ring.
func reasoningEffortIndex(efforts []modelregistry.ReasoningEffort, want modelregistry.ReasoningEffort) int {
	for index, effort := range efforts {
		if effort == want {
			return index
		}
	}
	return -1
}

// canonicalResponseStyleInput maps a /style argument to its stored English
// value, accepting the Portuguese display names as synonyms. Unknown values
// pass through unchanged so the caller can report them.
func canonicalResponseStyleInput(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "equilibrado":
		return "balanced"
	case "conciso":
		return "concise"
	case "explicativo":
		return "explanatory"
	case "revisão", "revisao":
		return "review"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// responseStyleLabel renders a stored style value with its Portuguese name.
func responseStyleLabel(value string) string {
	switch value {
	case "balanced":
		return "equilibrado"
	case "concise":
		return "conciso"
	case "explanatory":
		return "explicativo"
	case "review":
		return "revisão"
	default:
		return value
	}
}

func (m model) handleStyleCommand(args string) (model, string, error) {
	raw := strings.TrimSpace(args)
	arg := canonicalResponseStyleInput(raw)
	// Bare `/style` opens the picker at the dispatch layer, so it never
	// reaches here empty; keep the card as the fallback.
	if arg == "" || arg == "list" {
		return m, m.styleText(), nil
	}
	if !responseStyleAllowed(arg) {
		return m, "Estilo\nEstilo desconhecido: " + raw, nil
	}
	m.responseStyle = arg
	saveErr := m.saveResponseStyle()
	lines := []string{
		"Estilo",
		"estilo ativo: " + responseStyleLabel(m.responseStyle),
	}
	switch {
	case saveErr != nil:
		lines = append(lines, "nota: não foi possível salvar a preferência de estilo ("+saveErr.Error()+")")
	case strings.TrimSpace(m.userConfigPath) == "":
		lines = append(lines, "Vale só nesta sessão: sem caminho de config, a preferência não foi salva.")
	default:
		lines = append(lines, "Estilo: "+responseStyleLabel(m.responseStyle)+" · salvo")
	}
	return m, strings.Join(lines, "\n"), saveErr
}

// styleAppliedNotice returns the compact confirmation used after a successful
// style switch. The full state card remains available through /style list.
func (m model) styleAppliedNotice() string {
	if strings.TrimSpace(m.userConfigPath) == "" {
		return "Estilo: " + responseStyleLabel(m.responseStyle) + " · só nesta sessão"
	}
	return "Estilo: " + responseStyleLabel(m.responseStyle) + " · salvo"
}

// saveResponseStyle writes the active style to user config so it is applied
// again at startup (via Options.SavedResponseStyle). Best-effort: nil means
// saved, or session-only when there is no config path (not a failure).
func (m model) saveResponseStyle() error {
	if strings.TrimSpace(m.userConfigPath) == "" {
		return nil
	}
	_, err := config.SetResponseStyle(m.userConfigPath, m.responseStyle)
	return err
}

// styleResultNotice builds the transient confirmation for an applied style
// from the real persist outcome: a clean save (or session-only scope) keeps
// the success tone, while a failed write reports it with the warning tone.
func (m model) styleResultNotice(saveErr error) (string, transientNoticeTone) {
	if saveErr != nil {
		return "Estilo: " + responseStyleLabel(m.responseStyle) + " · não salvo (" + shortErrorText(saveErr) + ")", transientNoticeWarning
	}
	return m.styleAppliedNotice(), transientNoticeSuccess
}

// shortErrorText keeps only the first line of an error for inline notices.
func shortErrorText(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	return msg
}

func (m model) styleText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Estilo",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Estado",
			Lines: []string{
				"estilo ativo: " + responseStyleLabel(m.responseStyle),
				"disponíveis: balanced (equilibrado), concise (conciso), explanatory (explicativo), review (revisão)",
			},
		}},
		Hints: []string{"use /style sem argumento para abrir o seletor, ou /style <valor> para trocar direto"},
	})
}

func defaultedResponseStyle(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if responseStyleAllowed(value) {
		return value
	}
	return defaultResponseStyle
}

func (m model) handleSelfCorrectCommand(args string) (model, string) {
	args = strings.TrimSpace(strings.ToLower(args))
	switch args {
	case "", "status":
		return m, m.selfCorrectText()
	case "on", "tests", "full":
		m.selfCorrectTests = true
	case "off", "lsp":
		m.selfCorrectTests = false
	default:
		return m, "Self-correct\nUsage: /selfcorrect [status|on|off|tests|full|lsp]"
	}
	return m, m.selfCorrectText()
}

func (m model) selfCorrectText() string {
	depth := "LSP diagnostics only — fast, scoped to changed files"
	if m.selfCorrectTests {
		depth = "LSP diagnostics + project test plan (e.g. go test ./...) — slower, whole-repo"
	}
	return renderCommandOutput(commandOutput{
		Title:  "Self-correct",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "State",
			Lines: []string{
				"post-edit verification: " + depth,
				"auto-fix vs report-only: follows the active permission mode",
			},
		}},
		Hints: []string{"/selfcorrect on adds the full test suite; /selfcorrect off returns to LSP-only (default)"},
	})
}

func responseStyleAllowed(value string) bool {
	for _, style := range responseStyles {
		if value == style {
			return true
		}
	}
	return false
}

func (m model) handleCompactCommand(args string) (model, string, tea.Cmd) {
	args = strings.TrimSpace(strings.ToLower(args))
	if args == "status" {
		return m, m.compactText(false), nil
	}
	// Bare "/compact" already triggers compaction; accept "now" too since that is
	// what users reach for when they see the context gauge climbing.
	if args != "" && args != "now" {
		return m, "Compact\nusage: /compact [status|now]", nil
	}
	if m.compactInFlight {
		return m, m.compactText(true), nil
	}
	m.compactRequests++
	m.lastCompactError = ""
	m.lastCompactResult = nil
	if m.sessionCompactor == nil && !m.hasSessionBackedCompactor() {
		return m, m.compactText(true), nil
	}
	m.compactInFlight = true
	m.compactFrame = 0
	return m, m.compactText(true), tea.Batch(m.runCompact(), m.spinner.Tick)
}

func (m model) runCompact() tea.Cmd {
	request := m.compactRequest()
	if m.sessionCompactor != nil {
		compactor := m.sessionCompactor
		ctx := m.ctx
		return func() tea.Msg {
			result, err := compactor.CompactSession(ctx, request)
			return compactResultMsg{result: result, err: err}
		}
	}
	return func() tea.Msg {
		next, result, summaryUsage, err := m.compactActiveSession()
		if err != nil {
			return compactResultMsg{err: err}
		}
		msg := compactResultMsg{
			result:             result,
			activeSession:      next.activeSession,
			sessionEvents:      append([]sessions.Event{}, next.sessionEvents...),
			transcript:         append([]transcriptRow{}, next.transcript...),
			hasSessionSnapshot: true,
		}
		// The summarizer request is a real provider call: carry its usage
		// back to the Update loop, which records it through the same
		// tracker and provider_usage path as agent turns. The goroutine
		// must not touch model state itself. A provider that reports no
		// usage leaves no trace (no empty event).
		if summaryUsage.TotalTokens() > 0 {
			msg.compactionUsage = summaryUsage
			msg.compactionModel = request.ModelName
			msg.hasCompactionUsage = true
		}
		return msg
	}
}

// handleRewindCommand restores workspace files to a checkpoint and truncates the
// session log. "/rewind" or "/rewind latest" undoes the most recent checkpoint;
// "/rewind <n>" rewinds to a specific event sequence.
func (m model) handleRewindCommand(args string) (model, string) {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return m, "Rewind\nno active session to rewind."
	}
	if m.pending {
		return m, "Rewind\ncannot rewind while a run is in progress."
	}
	// The compaction result rewrites the session log and the in-memory
	// transcript; a rewind in between would be undone or applied to a log the
	// compaction is about to replace.
	if m.compactInFlight {
		return m, "Rewind\nCompactação em andamento. Tente de novo quando ela terminar."
	}
	// A cancelled run's late flush hasn't appended its checkpoint events yet:
	// ApplyRewind would prune those checkpoint blobs as unreferenced, then the
	// flush would re-append pre-rewind events after the rewind marker.
	if len(m.flushRunIDs) > 0 {
		return m, "Rewind\ncannot rewind while a cancelled run is still flushing — retry in a moment."
	}
	arg := strings.TrimSpace(strings.ToLower(args))
	target, err := m.resolveRewindTarget(arg)
	if err != nil {
		return m, "Rewind\n" + err.Error()
	}
	report, err := m.sessionStore.ApplyRewind(m.activeSession.SessionID, m.cwd, target)
	if err != nil {
		return m, "Rewind\n" + err.Error()
	}
	// The persisted log was truncated above the rewind point; stored real
	// history may reference events that no longer exist. Cleared here, before
	// the reload, so even a reload failure cannot leave the stale continuation
	// behind.
	m.continuation = turnContinuation{}
	if m.sessionStore != nil && m.activeSession.SessionID != "" {
		_ = m.sessionStore.InvalidateContinuation(m.activeSession.SessionID)
	}

	// ApplyRewind truncated the persisted event log, restored files, and appended a
	// rewind marker — so reload the in-memory session state. Otherwise the dropped
	// events would still (a) be re-sent to the agent as ContextEvents on the next
	// prompt (sessionPrompt sends m.sessionEvents) and (b) linger in the transcript.
	// A reload FAILURE must be surfaced (and the stale in-memory context dropped),
	// not ignored — silently keeping m.sessionEvents would re-send rewound-away
	// events on the next prompt, defeating the rewind.
	if meta, getErr := m.sessionStore.Get(m.activeSession.SessionID); getErr == nil {
		m.activeSession = *meta
	}
	events, readErr := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if readErr != nil {
		m.sessionEvents = nil // drop stale context so it can't reach the next prompt
		return m, fmt.Sprintf("Rewind\nrewound to sequence %d, but reloading the session failed (in-memory context cleared): %s", target, readErr.Error())
	}
	m.sessionEvents = append([]sessions.Event{}, events...)
	// The snapshot is rewritten from the post-rewind log (not deleted): a
	// resume right after still continues exact. Recovered checkpoints stay
	// valid — ApplyRewind prunes only blobs unreferenced by the surviving
	// events, and the rewrite covers the truncated head, so a later /rewind
	// to an earlier checkpoint still finds its blobs.
	m = m.rewriteSnapshotFromEvents()
	rows := initialTranscript()
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(events))
	m.transcript = rows
	// The rebuilt (post-rewind) transcript flushes fresh below a divider; the
	// pre-rewind scrollback above it stays, as scrollback cannot be un-printed.
	m.resetFlushFrontier("· rewound ·")

	summary := fmt.Sprintf("Rewound to sequence %d\n%d file(s) restored, %d deleted, %d skipped.",
		target, report.FilesRestored, report.FilesDeleted, len(report.Skipped))
	if len(report.Skipped) > 0 {
		summary += "\nskipped (not recoverable): " + strings.Join(report.Skipped, ", ")
	}
	return m, summary
}

// resolveRewindTarget maps a /rewind argument to a keep-through event sequence.
func (m model) resolveRewindTarget(arg string) (int, error) {
	events, err := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if err != nil {
		return 0, err
	}
	if arg == "" || arg == "latest" {
		lastCheckpoint := 0
		for _, ev := range events {
			if ev.Type == sessions.EventSessionCheckpoint && ev.Sequence > lastCheckpoint {
				lastCheckpoint = ev.Sequence
			}
		}
		if lastCheckpoint == 0 {
			return 0, fmt.Errorf("no checkpoints to rewind")
		}
		return lastCheckpoint - 1, nil // undo the most recent checkpoint
	}
	seq, err := strconv.Atoi(arg)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("usage: /rewind [latest|<sequence>]")
	}
	return seq, nil
}

func (m model) compactText(requested bool) string {
	if m.compactInFlight {
		return m.compactRunningText()
	}
	if requested && m.lastCompactResult != nil {
		return compactCompleteText(*m.lastCompactResult)
	}
	status := commandStatusInfo
	if requested {
		status = commandStatusWarning
		if m.lastCompactResult != nil {
			status = commandStatusOK
		}
		if m.lastCompactError != "" {
			status = commandStatusBlocked
		}
	}
	request := m.compactRequest()
	sections := []commandSection{
		{
			Title: "State",
			Lines: []string{
				"summary: " + m.compactionStatus(),
				"requested: " + boolText(m.compactRequests > 0),
				"model: " + displayValue(request.ModelName, "none"),
				"context window: " + compactContextWindowText(request.ContextWindow),
				"auto compaction: " + m.autoCompactSummary() + " (/autocompact)",
				fmt.Sprintf("estimated transcript: %d tokens", request.EstimatedTokens),
				fmt.Sprintf("visible transcript rows: %d", request.VisibleTranscriptRows),
				m.compactabilityText(request),
			},
		},
	}
	if m.lastCompactResult != nil {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: compactResultLines(*m.lastCompactResult),
		})
	} else if m.lastCompactError != "" {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: []string{
				"compacted: no",
				"error: " + m.lastCompactError,
			},
		})
	} else if requested && m.sessionCompactor == nil {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: []string{
				"compacted: no",
				"reason: manual compactor unavailable",
			},
		})
	}
	return renderCommandOutput(commandOutput{
		Title:    "Compact",
		Status:   status,
		Sections: sections,
		Hints:    []string{"manual compaction affects this TUI session when a compactor is available"},
	})
}

func (m model) compactRunningText() string {
	return strings.Join([]string{
		"Compressing session",
		"Keep editing your draft; press Enter after compression finishes to send.",
		m.compactAnimationLine(),
	}, "\n")
}

func compactCompleteText(result CompactResult) string {
	lines := []string{
		"Compression complete",
		"Session summary saved.",
		"Ready for the next prompt.",
	}
	if result.Compacted && result.BeforeTokens > 0 && result.AfterTokens > 0 && result.AfterTokens < result.BeforeTokens {
		lines = append(lines, fmt.Sprintf("Estimated context reduced by %s tokens.", formatContextWindow(result.BeforeTokens-result.AfterTokens)))
	}
	return strings.Join(lines, "\n")
}

func (m model) compactionStatus() string {
	if m.compactInFlight {
		return "compacting context"
	}
	if m.lastCompactResult != nil {
		if m.lastCompactResult.Compacted {
			return "compacted manually"
		}
		return "manual compactor completed without changes"
	}
	if m.lastCompactError != "" {
		return "manual compaction failed"
	}
	if m.compactRequests > 0 {
		return "requested, awaiting manual compactor"
	}
	return "not compacted"
}

func (m model) compactRequest() CompactRequest {
	return CompactRequest{
		SessionID:             strings.TrimSpace(m.activeSession.SessionID),
		ModelName:             strings.TrimSpace(m.modelName),
		ContextWindow:         m.modelContextWindow(m.modelName),
		SessionEventCount:     len(m.sessionEvents),
		EstimatedTokens:       estimateTranscriptTokens(m.transcript),
		VisibleTranscriptRows: len(m.transcript),
		CompactRequests:       m.compactRequests,
	}
}

func (m model) compactabilityText(request CompactRequest) string {
	if m.sessionCompactor == nil && !m.hasSessionBackedCompactor() {
		return "compactable: no (manual compactor unavailable)"
	}
	if m.sessionCompactor == nil && request.SessionEventCount <= tuiCompactionPreserveLast {
		return "compactable: no (not enough session history yet)"
	}
	if request.ContextWindow <= 0 {
		return "compactable: no (unknown model context window)"
	}
	if request.EstimatedTokens <= 0 {
		return "compactable: no (empty transcript)"
	}
	return "compactable: yes"
}

func (m model) hasSessionBackedCompactor() bool {
	return m.sessionStore != nil && strings.TrimSpace(m.activeSession.SessionID) != ""
}

func (m model) compactActiveSession() (model, CompactResult, qgruntime.Usage, error) {
	if m.sessionStore == nil || strings.TrimSpace(m.activeSession.SessionID) == "" {
		return m, CompactResult{}, qgruntime.Usage{}, fmt.Errorf("no active session to compact")
	}
	beforeEvents := append([]sessions.Event{}, m.sessionEvents...)
	beforeTokens := estimateTranscriptTokens(m.transcript)
	plan, err := m.sessionStore.PlanCompaction(m.activeSession.SessionID, sessions.CompactionOptions{
		PreserveLast: tuiCompactionPreserveLast,
		SkipPrompt:   true,
	})
	if err != nil {
		return m, CompactResult{}, qgruntime.Usage{}, err
	}
	if plan.CompactableCount == 0 {
		return m, CompactResult{
			Compacted:    false,
			BeforeTokens: beforeTokens,
			AfterTokens:  beforeTokens,
			Summary:      "not enough session history to compact yet",
		}, qgruntime.Usage{}, nil
	}

	rawEvents, err := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if err != nil {
		return m, CompactResult{}, qgruntime.Usage{}, fmt.Errorf("read session events for compaction: %w", err)
	}
	compactableEvents, err := sessionEventsForRefs(rawEvents, plan.CompactableEvents)
	if err != nil {
		return m, CompactResult{}, qgruntime.Usage{}, err
	}
	summaryResult, summaryUsage, err := m.summarizeCompactionPlan(plan, sessions.CompactionMessages(compactableEvents))
	if err != nil {
		return m, CompactResult{}, qgruntime.Usage{}, err
	}
	// PromptChars counts the text content sent to the provider: the shared
	// system instruction plus projected message/tool payloads. Provider protocol
	// framing is intentionally excluded.
	plan.PromptChars = len(agent.CompactionSummaryInstructions) + summaryResult.ProjectedChars
	plan.Truncated = summaryResult.Truncated
	if _, err := m.sessionStore.RecordCompaction(m.activeSession.SessionID, sessions.RecordCompactionInput{
		Plan:    plan,
		Summary: summaryResult.SummaryText,
	}); err != nil {
		return m, CompactResult{}, qgruntime.Usage{}, err
	}
	if meta, err := m.sessionStore.Get(m.activeSession.SessionID); err == nil && meta != nil {
		m.activeSession = *meta
	}
	events, err := m.sessionStore.ReadRehydratedEvents(m.activeSession.SessionID)
	if err != nil {
		m.sessionEvents = nil
		return m, CompactResult{}, qgruntime.Usage{}, fmt.Errorf("reload compacted session: %w", err)
	}
	m.sessionEvents = append([]sessions.Event{}, events...)
	rows := initialTranscript()
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(events))
	m.transcript = rows
	m.resetFlushFrontier("· compactado ·")

	return m, CompactResult{
		Compacted:    len(events) < len(beforeEvents)+1,
		BeforeTokens: beforeTokens,
		AfterTokens:  estimateTranscriptTokens(m.transcript),
		Summary:      summaryResult.SummaryText,
	}, summaryUsage, nil
}

func sessionEventsForRefs(events []sessions.Event, refs []sessions.EventRef) ([]sessions.Event, error) {
	byID := make(map[string]sessions.Event, len(events))
	for _, event := range events {
		byID[event.ID] = event
	}
	selected := make([]sessions.Event, 0, len(refs))
	for _, ref := range refs {
		event, ok := byID[ref.ID]
		if !ok || event.Sequence != ref.Sequence || event.Type != ref.Type {
			return nil, fmt.Errorf("session changed while compaction was being planned; retry /compact")
		}
		selected = append(selected, event)
	}
	return selected, nil
}

func (m model) summarizeCompactionPlan(plan sessions.CompactionPlan, messages []qgruntime.Message) (agent.CompactionSummaryResult, qgruntime.Usage, error) {
	var promptChars int
	var summaryUsage qgruntime.Usage
	result, err := agent.SummarizeCompactionMessages(messages, func(projected []qgruntime.Message) (string, error) {
		if m.provider == nil {
			return deterministicCompactionSummary(plan), nil
		}
		// Build the summarizer request exactly like the agent's automatic
		// compaction: [system, user] with the conversation rendered to plain
		// text. Forwarding the projected wire messages can end the request
		// on an assistant tool call or a tool result; with no tools declared
		// the provider then answers with empty text.
		requestMessages := agent.CompactionSummaryRequestMessages(projected)
		promptChars = compactionRequestChars(requestMessages)
		stream, streamErr := m.provider.StreamCompletion(m.ctx, qgruntime.CompletionRequest{
			Messages: requestMessages,
		})
		if streamErr != nil {
			return "", fmt.Errorf("summarize compacted session: %w", streamErr)
		}
		collected := qgruntime.CollectStream(m.ctx, stream)
		if collected.Error != "" {
			return "", fmt.Errorf("summarize compacted session: %s", collected.Error)
		}
		// The summary request is a compaction-summarizer call like the
		// agent's automatic compaction: tag it so its cost still counts
		// while the session-cache stats skip it.
		summaryUsage = collected.Usage
		summaryUsage.Summarizer = true
		result := strings.TrimSpace(collected.Text)
		if result == "" {
			return "", fmt.Errorf("summarize compacted session: empty summary")
		}
		return result, nil
	})
	if err != nil {
		return result, qgruntime.Usage{}, err
	}
	if promptChars > 0 {
		// ProjectedChars feeds plan.PromptChars below: count what was actually
		// sent (system instruction plus rendered user message), not the
		// projected wire messages.
		result.ProjectedChars = promptChars - len(agent.CompactionSummaryInstructions)
	}
	return result, summaryUsage, nil
}

// compactionRequestChars counts the text content of a provider request,
// excluding protocol framing.
func compactionRequestChars(messages []qgruntime.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Name) + len(call.Arguments)
		}
	}
	return total
}

func deterministicCompactionSummary(plan sessions.CompactionPlan) string {
	lines := []string{
		fmt.Sprintf("Compacted earlier session context: %d event(s) summarized, %d recent event(s) preserved.", plan.CompactableCount, plan.PreservedCount),
	}
	if len(plan.CompactableEvents) > 0 {
		first := plan.CompactableEvents[0]
		last := plan.CompactableEvents[len(plan.CompactableEvents)-1]
		lines = append(lines, fmt.Sprintf("Compacted range: #%d %s through #%d %s.", first.Sequence, first.Type, last.Sequence, last.Type))
	}
	if len(plan.PreservedEvents) > 0 {
		first := plan.PreservedEvents[0]
		last := plan.PreservedEvents[len(plan.PreservedEvents)-1]
		lines = append(lines, fmt.Sprintf("Preserved recent range: #%d %s through #%d %s.", first.Sequence, first.Type, last.Sequence, last.Type))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func compactContextWindowText(window int) string {
	if window <= 0 {
		return "unknown"
	}
	return formatContextWindow(window) + " tokens"
}

func estimateTranscriptTokens(rows []transcriptRow) int {
	total := 0
	for _, row := range rows {
		text := row.text + "\n" + row.detail + "\n" + row.tool + "\n" + row.arg
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		total += agent.ApproxTextTokens(text) + 4
	}
	return total
}

func compactResultLines(result CompactResult) []string {
	lines := []string{
		"compacted: " + boolText(result.Compacted),
	}
	if result.BeforeTokens > 0 {
		lines = append(lines, fmt.Sprintf("before: %d tokens", result.BeforeTokens))
	}
	if result.AfterTokens > 0 {
		lines = append(lines, fmt.Sprintf("after: %d tokens", result.AfterTokens))
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		lines = append(lines, "summary: "+summary)
	}
	return lines
}

func (m model) compactAnimationLine() string {
	frame := compactFrames[m.compactFrame%len(compactFrames)]
	return frame + " Compressing history..."
}

func (m model) setCompactStatusRow(text string) model {
	row := transcriptRow{kind: rowSystem, id: compactStatusRowID, text: text}
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].id == compactStatusRowID {
			m.transcript[i] = row
			// Settled alt-screen rows are cached across frames; force a rebuild
			// so an in-place update (e.g. the compact spinner tick) is reflected
			// immediately instead of serving the stale cached snapshot.
			if i < m.flushed {
				m.altScreenSettledWidth = 0
			}
			return m
		}
	}
	m.transcript = appendTranscriptRow(m.transcript, row)
	return m
}

// recordCompactionUsage records the compaction summarizer's provider call
// through the same path as agent turns: the usage tracker (so the call's
// cost counts) plus a provider_usage session event in the agent's format,
// labeled with the model id like every other call. The summarizer tag is
// what identifies the compaction; it keeps the call out of the
// session-cache stats, exactly like the agent's automatic compaction. The
// receipt row goes to the live transcript too, so the live view matches
// the resumed one. Must run on the Update loop, never in runCompact's
// goroutine.
func (m model) recordCompactionUsage(modelID string, event qgruntime.Usage) model {
	callModelID := strings.TrimSpace(event.ModelID)
	if callModelID == "" {
		callModelID = strings.TrimSpace(modelID)
	}
	if callModelID == "" {
		callModelID = strings.TrimSpace(m.modelName)
	}
	if callModelID == "" {
		return m
	}
	event.Summarizer = true
	var info qgaccount.ModelInfo
	if found := m.apiModel(callModelID); found != nil {
		info = *found
	}
	receipt := callUsageRow(0, m.compactRequests, callModelID, event, info)
	m.transcript = appendTranscriptRow(m.transcript, receipt)
	m, usageRows := m.recordUsageEvent(callModelID, event, receipt)
	for _, row := range usageRows {
		m.transcript = appendTranscriptRow(m.transcript, row)
	}
	payload := usage.EventUsagePayload(event)
	payload["model"] = callModelID
	payload["summarizer"] = true
	payload["call_id"] = receipt.id
	payload["call_label"] = receipt.text
	payload["call_tokens"] = receipt.meta["tokens"]
	payload["cost_exact"] = receipt.meta["amount"]
	payload["cost_currency"] = receipt.meta["currency"]
	payload["call_elapsed_ns"] = event.Elapsed.Nanoseconds()
	var err error
	m, err = m.appendSessionEvent(sessions.EventUsage, payload)
	if err != nil {
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowError, text: "erro ao gravar a sessão: " + err.Error()})
	}
	return m
}

func (m model) recordUsageEvent(modelID string, event qgruntime.Usage, receipt transcriptRow) (model, []transcriptRow) {
	if m.usageTracker == nil || strings.TrimSpace(modelID) == "" {
		return m, nil
	}
	normalized, runtimeUsage, err := usage.Normalize(event)
	if err != nil {
		return m, []transcriptRow{{kind: rowError, text: "usage: " + err.Error()}}
	}
	if event.ModelID != "" {
		modelID = event.ModelID
	}
	m.summarizerRecordedLast = event.Summarizer
	if !event.Summarizer {
		m.lastUsage = normalized
		if receipt.meta == nil {
			receipt.meta = map[string]string{"model": modelID}
		}
		if _, ok := receipt.meta["model"]; !ok {
			receipt.meta["model"] = modelID
		}
		m.lastUsageReceipt = receipt
		m.lastUsageSeen = true
		m.meterLive = true
		m.postCompactTokens = 0
		m.hasPostCompactTokens = false
	}
	callsBefore := len(m.cacheCalls)
	boundary := m.cacheClearBoundary
	m.cacheClearBoundary = false
	// Summarizer calls keep their cost accounting below but never enter the
	// cache statistics: no drop, no hit/economy share, no last-call memory.
	if !event.Summarizer {
		m = m.trackCacheUsage(normalized, receipt, callsBefore, boundary)
	}
	if record, err := m.usageTracker.Record(usage.RecordInput{
		ModelID: modelID,
		Usage:   runtimeUsage,
		Source:  "tui",
	}); err != nil {
		if isUnpricedUsageError(err) {
			m.unpricedRequests++
			m.unpricedTokens += normalized.TotalTokens
			return m, nil
		}
		return m, []transcriptRow{{kind: rowError, text: "usage: " + err.Error()}}
	} else if record.Cost.Currency == "" {
		// The tracker records gateway models missing from the curated catalog
		// with an explicitly unknown cost instead of failing. Count their
		// tokens through the unpriced path so the summary reads "cost
		// unavailable" rather than a fabricated $0.00.
		m.unpricedRequests++
		m.unpricedTokens += normalized.TotalTokens
		return m, nil
	}
	return m, nil
}

func compactedContextTokens(messages []qgruntime.Message, events []sessions.Event, fallback int) int {
	if len(messages) > 0 {
		if n := agent.MeasureContext(messages, nil, 0).MessageTokens; n > 0 {
			return n
		}
	}
	if len(events) > 0 {
		rebuilt := sessions.RepairOrphanToolCalls(sessions.ReconstructContinuationMessages(events))
		if n := agent.MeasureContext(rebuilt, nil, 0).MessageTokens; n > 0 {
			return n
		}
	}
	if fallback > 0 {
		return fallback
	}
	return 0
}

func (m model) contextFootprintTokens() int {
	if m.hasPostCompactTokens {
		return m.postCompactTokens
	}
	if m.lastUsageSeen && m.lastUsage.TotalTokens > 0 {
		return m.lastUsage.TotalTokens
	}
	return 0
}

func (m model) latestUsageTokens(summary usage.Summary) int {
	if m.hasPostCompactTokens && m.postCompactTokens > 0 {
		return m.postCompactTokens
	}
	if m.lastUsageSeen && m.lastUsage.TotalTokens > 0 {
		return m.lastUsage.TotalTokens
	}
	if !m.summarizerRecordedLast && summary.LastRecord != nil && summary.LastRecord.Usage.TotalTokens > 0 {
		return summary.LastRecord.Usage.TotalTokens
	}
	if !m.hasPostCompactTokens && !m.summarizerRecordedLast {
		return m.unpricedTokens
	}
	return 0
}

func isUnpricedUsageError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unknown qg model",
		"missing model input pricing rate",
		"missing model output pricing rate",
		"invalid model cached input pricing rate",
		"no model cost tier covers",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (m model) usageSummaryText() string {
	if m.usageTracker == nil {
		return "uso indisponível"
	}
	summary := m.usageTracker.Summary()
	if summary.RecordCount == 0 && m.unpricedRequests == 0 {
		return "ainda sem uso"
	}
	if summary.RecordCount == 0 {
		return formatUnpricedUsage(m.unpricedRequests, m.unpricedTokens)
	}
	if m.unpricedRequests == 0 {
		return usage.FormatSummary(summary)
	}
	return usage.FormatSummary(summary) + "; " + formatUnpricedUsage(m.unpricedRequests, m.unpricedTokens)
}

// cacheEfficiencyText reports the session's prompt-cache hit rate for /context so a
// user can see whether cache reads are actually saving work. A low rate across
// several turns usually means the cacheable prefix is churning (e.g. a tool list
// that shifts mid-session), so it flags that case to make slowness diagnosable.
func (m model) cacheEfficiencyText() string {
	if m.usageTracker == nil {
		return "uso indisponível"
	}
	summary := m.usageTracker.Summary()
	if summary.InputTokens <= 0 {
		return "n/a"
	}
	text := usage.FormatCacheEfficiency(summary)
	if summary.RecordCount > 5 && summary.CacheHitRate() < 0.5 {
		text += " — baixa; o prefixo pode estar instável"
	}
	return text
}

func formatUnpricedUsage(requests int, tokens int) string {
	requestLabel := "chamadas"
	if requests == 1 {
		requestLabel = "chamada"
	}
	return fmt.Sprintf("%d %s, %d tokens, custo indisponível", requests, requestLabel, tokens)
}
