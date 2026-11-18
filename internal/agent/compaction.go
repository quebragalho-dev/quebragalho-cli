package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/trace"
)

// Session compaction.
//
// When a running conversation approaches the model's context window, the oldest
// middle of the history is summarized into a single user-role note while the
// system prompt(s) and the most recent turns are kept verbatim. This keeps long
// sessions from blowing the context budget.
//
// Compact is a PURE function: it never talks to a provider. The actual LLM call
// is injected via CompactionOptions.Summarize so it stays trivially testable and
// the agent loop owns the provider wiring.

// defaultCompactionPreserveLast is how many trailing messages are kept verbatim
// when the caller does not specify a preserve count. Kept in sync with the replay
// reconstruction default in internal/sessions/replay.go.
const defaultCompactionPreserveLast = 6

// compactionTriggerRatio is the fraction of the context window at which
// proactive compaction kicks in (top of each turn). It is the automatic
// trigger and also the ceiling for a user limit. NOTE: this is speculative
// tuning — validate against the per-turn latency/compaction metrics (roadmap 5.2)
// before trusting it. Measured fixed overhead is ~7.4k tokens (not the ~16k the
// roadmap assumed), so context fills slower; if data shows 0.7 triggers redundant
// summarization, raise it back toward 0.8.
const compactionTriggerRatio = 0.7

// summaryLabel prefixes the injected summary so it is unmistakable in the
// transcript (and so tests can assert on it).
const summaryLabel = "[Summary of earlier conversation]"

// CompactionSummaryInstructions is the system prompt handed to both automatic
// and manually requested compaction summarizers.
const CompactionSummaryInstructions = "You are compacting a coding-assistant conversation to save context. " +
	"Write a dense, factual summary of the conversation so far. Preserve: the user's goals and explicit constraints; " +
	"decisions made and why; files created or modified (with paths) and key code changes; commands run and their important " +
	"results; and anything still in progress or unresolved. Omit pleasantries. Use terse bullet points. Do not invent details. " +
	"If the conversation already begins with an earlier summary block, treat its facts as established context and carry them " +
	"forward into the new summary — never drop earlier information."

// CompactionOptions configure a single Compact call.
type CompactionOptions struct {
	// PreserveLast is the number of trailing messages to keep verbatim. The
	// preserved suffix is widened (never shrunk) so it begins at a safe
	// user/assistant boundary. <= 0 falls back to defaultCompactionPreserveLast.
	PreserveLast int
	// Summarize turns the to-be-elided middle into a single dense summary. It is
	// injected so Compact stays pure and testable; the agent loop wires it to a
	// real provider call.
	Summarize func(toSummarize []qgruntime.Message) (string, error)
	// taskState is a snapshot supplied by the running agent. Its immutable
	// objective is always preserved; mutable fields are admitted only when its
	// plan projection still matches the transcript.
	taskState *taskStateSnapshot
}

// CompactionResult is the metadata-bearing result returned by CompactMessages.
type CompactionResult struct {
	// Messages is the original conversation or the compacted replacement.
	Messages []qgruntime.Message
	// RemovedCount is the number of original messages summarized away.
	RemovedCount int
	// PreservedCount is the number of original messages kept verbatim, including
	// leading system messages and the preserved suffix.
	PreservedCount int
	// SummaryText is the trimmed text returned by the summarizer. The injected
	// summary message also includes summaryLabel and any preserved structured
	// state needed by later compactions.
	SummaryText string
	// ProjectedChars is the text content sent to the summarizer after semantic
	// projection, excluding provider protocol framing.
	ProjectedChars int
	// Truncated reports whether projection dropped content to satisfy its
	// bounded brief or previous-summary limits.
	Truncated bool
	// Compacted reports whether Messages contains an injected summary.
	Compacted bool
}

// imageTokenEstimate is a flat per-image token cost. Vision models tokenize an
// image by its resolution (tiles), not its byte length, so the raw Data size is
// a poor proxy — counting len(Data)/4 would overcount by orders of magnitude.
// A fixed estimate keeps estimateTokens monotonic in the image count so an
// image-heavy context still trends toward compaction instead of reading as ~0.
// ~1k tokens is a representative mid-range cost across providers.
const imageTokenEstimate = 1000

// ApproxTextTokens estimates the token count of text WITHOUT a real tokenizer.
// The BPE tokenizers qg targets fold a run's leading space into the following
// token rather than emitting whitespace as its own token, so naive len/4
// overcounts real text by ~15-20% (measured: a 24.8k-char prompt counted 5.2k
// real tokens where len/4 said 6.2k). Counting NON-whitespace bytes / 4 tracks
// the provider's actual count closely (validated against live usage) while
// staying allocation- and dependency-free. qg still receives the exact count
// back as usage on every request; this estimate is only for the pre-request
// context budget preview and the compaction threshold.
func ApproxTextTokens(value string) int {
	nonSpace := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
		default:
			nonSpace++
		}
	}
	return nonSpace / 4
}

// estimateTokens is a cheap, dependency-free token estimate (see ApproxTextTokens)
// across message content plus tool call names/arguments and a flat per-image
// cost. It deliberately uses no real tokenizer; it only needs to be monotonic
// and roughly proportional so the loop can decide when to compact.
func estimateTokens(messages []qgruntime.Message) int {
	total := 0
	for _, message := range messages {
		total += ApproxTextTokens(message.Content)
		for _, call := range message.ToolCalls {
			total += ApproxTextTokens(call.Name)
			total += ApproxTextTokens(call.Arguments)
			total += 4 // small per-call overhead
		}
		total += len(message.Images) * imageTokenEstimate
		total += 4 // per-message overhead
	}
	return total
}

// estimateToolDefTokens approximates the input-token cost of the tool definitions
// sent with every request (~4 chars/token, matching estimateTokens). Compaction
// must include them: they ride on every turn, so ignoring them under-counts the
// real context and can let it blow past the model limit while the message-only
// estimate still looks under threshold.
func estimateToolDefTokens(tools []qgruntime.ToolDefinition) int {
	total := 0
	for _, tool := range tools {
		total += ApproxTextTokens(tool.Name)
		total += ApproxTextTokens(tool.Description)
		total += ApproxTextTokens(string(tool.Type))
		if len(tool.Parameters) > 0 {
			if encoded, err := json.Marshal(tool.Parameters); err == nil {
				total += ApproxTextTokens(string(encoded))
			}
		}
		if tool.Format != nil {
			total += ApproxTextTokens(tool.Format.Type)
			total += ApproxTextTokens(tool.Format.Syntax)
			total += ApproxTextTokens(tool.Format.Definition)
		}
		total += 4 // per-tool overhead
	}
	return total
}

// compactionThreshold is the estimated-token level at which proactive
// compaction triggers for a given context window.
func compactionThreshold(contextWindow int) int {
	if contextWindow <= 0 {
		return 0
	}
	return int(float64(contextWindow) * compactionTriggerRatio)
}

// autoCompactThreshold is the proactive trigger for a window and a user
// limit (Options.AutoCompactLimit). A positive limit below the automatic
// threshold wins; anything else (automatic, or a limit the window cannot
// hold) falls back to the automatic threshold. A negative limit (off) is
// enforced by maybeCompact, not here.
func autoCompactThreshold(contextWindow int, limit int) int {
	automatic := compactionThreshold(contextWindow)
	if limit > 0 && limit < automatic {
		return limit
	}
	return automatic
}

// AutoCompactThreshold reports where proactive compaction triggers for a
// context window and a user limit (Options.AutoCompactLimit). It returns 0
// when the limit turns proactive compaction off or the window is unknown.
func AutoCompactThreshold(contextWindow int, limit int) int {
	if limit < 0 {
		return 0
	}
	return autoCompactThreshold(contextWindow, limit)
}

// Compact summarizes the oldest middle of a conversation, keeping the leading
// system message(s) and the most recent turns verbatim. The result is:
//
//	[system..., summaryAsUser, preservedSuffix...]
//
// Rules:
//   - Leading system messages stay at the head untouched.
//   - The preserved suffix is widened backward so it never begins with a
//     tool/tool_result message (provider APIs reject a dangling tool result
//     with no preceding assistant tool call).
//   - The summary is injected as a single user-role message labeled with
//     summaryLabel.
//   - If there is nothing to summarize (too few messages once system and the
//     preserved suffix are removed), the input is returned unchanged.
//
// Compact is pure: it performs no provider I/O. A Summarize error is returned to
// the caller, which decides how to recover.
func Compact(messages []qgruntime.Message, opts CompactionOptions) ([]qgruntime.Message, error) {
	result, err := CompactMessages(messages, opts)
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// CompactMessages summarizes the oldest middle of a conversation and returns
// both the replacement messages and UI/session-friendly metadata about what
// changed. It uses the same compaction rules as Compact.
func CompactMessages(messages []qgruntime.Message, opts CompactionOptions) (CompactionResult, error) {
	preserveLast := opts.PreserveLast
	if preserveLast <= 0 {
		preserveLast = defaultCompactionPreserveLast
	}
	if opts.Summarize == nil {
		return CompactionResult{}, errors.New("compaction requires a Summarize function")
	}

	// Leading system messages are kept verbatim at the head.
	systemEnd := 0
	for systemEnd < len(messages) && messages[systemEnd].Role == qgruntime.MessageRoleSystem {
		systemEnd++
	}

	// Naive boundary: keep the last preserveLast messages. Then widen the suffix
	// backward to a safe boundary so it never starts on a tool result.
	boundary := len(messages) - preserveLast
	if boundary < systemEnd {
		boundary = systemEnd
	}
	boundary = safeSuffixBoundary(messages, systemEnd, boundary)

	middle := messages[systemEnd:boundary]
	if len(middle) == 0 {
		// Nothing to summarize once system + preserved suffix are removed.
		return CompactionResult{
			Messages:       messages,
			PreservedCount: len(messages),
		}, nil
	}

	summaryResult, err := SummarizeCompactionMessages(middle, opts.Summarize)
	if err != nil {
		return CompactionResult{}, err
	}
	summary := summaryResult.SummaryText

	// Preserve structured state (active plan + loaded skills) from the elided
	// middle verbatim, so it is not lost or paraphrased away by the prose summary.
	content := appendPreservedState(summaryLabel+"\n"+summary, middle, opts.taskState)

	compacted := make([]qgruntime.Message, 0, systemEnd+1+(len(messages)-boundary))
	compacted = append(compacted, messages[:systemEnd]...)
	compacted = append(compacted, qgruntime.Message{
		Role:    qgruntime.MessageRoleUser,
		Content: content,
	})
	compacted = append(compacted, messages[boundary:]...)
	return CompactionResult{
		Messages:       compacted,
		RemovedCount:   len(middle),
		PreservedCount: len(messages) - len(middle),
		SummaryText:    summary,
		ProjectedChars: summaryResult.ProjectedChars,
		Truncated:      summaryResult.Truncated,
		Compacted:      true,
	}, nil
}

// CompactionSummaryResult describes the shared summarization input and output.
type CompactionSummaryResult struct {
	SummaryText    string
	ProjectedChars int
	Truncated      bool
}

// SummarizeCompactionMessages applies the shared semantic projection before
// invoking a caller-supplied summarizer. Automatic context-pressure compaction
// and manual session compaction both use this path.
func SummarizeCompactionMessages(messages []qgruntime.Message, summarize func([]qgruntime.Message) (string, error)) (CompactionSummaryResult, error) {
	if summarize == nil {
		return CompactionSummaryResult{}, errors.New("compaction requires a Summarize function")
	}
	projection := projectCompactionInput(messages)
	summaryInput := projection.messages
	if len(summaryInput) == 0 {
		summaryInput = messages
	}
	summary, err := summarize(summaryInput)
	if err != nil {
		return CompactionSummaryResult{}, err
	}
	return CompactionSummaryResult{
		SummaryText:    strings.TrimSpace(summary),
		ProjectedChars: compactionMessageChars(summaryInput),
		Truncated:      projection.truncated,
	}, nil
}

func compactionMessageChars(messages []qgruntime.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Name) + len(call.Arguments)
		}
	}
	return total
}

// safeSuffixBoundary walks the preserve boundary backward (toward systemEnd) so
// the preserved suffix begins on a user or assistant message rather than a
// tool/tool_result message. A tool result with no preceding assistant tool call
// is rejected by provider APIs, so the boundary must land on a safe turn start.
// It never moves the boundary forward (the suffix only grows), and never crosses
// systemEnd.
func safeSuffixBoundary(messages []qgruntime.Message, systemEnd int, boundary int) int {
	// Walk back so the preserved suffix begins with an assistant message. The
	// summary is injected as a user-role message, so a user- or tool-led suffix
	// would create consecutive same-role turns that strict providers (Anthropic)
	// reject. Stopping on an assistant keeps user/assistant alternation valid;
	// if no assistant exists above systemEnd, boundary lands at systemEnd and the
	// middle is empty, so Compact no-ops (no summary is injected).
	for boundary > systemEnd && messages[boundary].Role != qgruntime.MessageRoleAssistant {
		boundary--
	}
	return boundary
}

// isContextLimitError reports whether a provider error string looks like a
// context-window / prompt-too-long error from a common provider. Matching is
// substring-based and case-insensitive so it tolerates phrasing differences
// across OpenAI, Anthropic, and Google providers.
func isContextLimitError(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" {
		return false
	}
	needles := []string{
		"context length",
		"context window",
		"context_length_exceeded",
		"maximum context",
		"context limit",
		"prompt is too long",
		"too many tokens",
		"reduce the length of the messages",
		"exceeds the maximum",
		"input is too long",
	}
	for _, needle := range needles {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

// isStreamTimeoutError reports whether a streamed error is a provider stream
// idle/stall timeout (providerio surfaces these as "idle timeout after …" /
// "no output for …" / "stream stalled"). Such a turn produced nothing, so when
// no content was forwarded yet it can be safely re-issued on a fresh connection
// (see the stall-retry in the agent loop). Substring + case-insensitive to
// tolerate the prefixed "provider stream error: …" wrapping.
func isStreamTimeoutError(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" {
		return false
	}
	for _, needle := range []string{"idle timeout after", "no output for", "stream stalled"} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

// compactionState carries the per-run state the agent loop needs to compact a
// conversation safely. It is created once per Run and is a no-op whenever
// options.ContextWindow <= 0.
//
// The run's compaction lifecycle is an explicit phase machine (compactPhase +
// compactTransitionTable below): lowWaterMark, reactiveAttempted and enabled
// are written ONLY by transition, so the one-shot budget and the watermark
// can never drift apart in one path and not another.
type compactionState struct {
	enabled   bool
	threshold int
	// limit is the user limit the threshold is derived from; kept so a
	// window change re-derives it.
	limit        int
	preserveLast int
	// phase is the explicit lifecycle phase; written only by transition.
	phase compactPhase
	// lowWaterMark is the estimated token size at (or below) which we will NOT
	// proactively compact again. It is the size right after the last compaction;
	// the loop only compacts when the history has grown past it AND is over the
	// threshold. This prevents compacting on every turn once near the limit.
	lowWaterMark int
	// reactiveAttempted guards the reactive path so it fires at most once per
	// run. Without this a provider that keeps returning context-limit errors
	// (even after compaction) could loop indefinitely; one attempt then the
	// original error surfaces.
	reactiveAttempted bool
	// onUsage forwards the summarizer call's token usage for accounting/budgeting.
	// OnText is deliberately NOT forwarded (compaction stays invisible to the user),
	// but its token COST must still be counted so usage reports and budgets include it.
	onUsage func(Usage)
	task    *taskState
	planner *contextPlanner
	trace   *trace.Recorder
	// onCompacted fires after the history was actually rewritten (paid
	// summarization or a free prune stage that shrank it). Surfaces use it to
	// explain a cache miss on the next call. Nil disables it.
	onCompacted func()

	// calibrationRatio scales the raw byte/4 token estimate toward the provider's
	// real prompt-token count. ApproxTextTokens over-counts code-heavy content by
	// ~15-20%, which would trip compaction early (at ~60% of true capacity). It
	// starts at 1.0 and converges via an EMA as each turn reports actual usage, so
	// later turns compact nearer to real capacity. QG is treated as 1.0.
	calibrationRatio float64

	// summarizerFactory lazily builds the dedicated (cheap) summarizer provider
	// (Options.Summarizer) for the current main model. summarizerProvider
	// memoizes the built provider and summarizerResolved records that the
	// factory ran (it may legitimately return no provider: the main model is
	// already the cheap one). summarizerBroken is the sticky fallback: once a
	// build or call fails, the run stays on the main provider until the next
	// model switch — a misconfigured cheap model must never break (or
	// repeatedly delay) compaction.
	summarizerFactory  func(ctx context.Context, mainModelID string) (Provider, error)
	summarizerProvider Provider
	summarizerResolved bool
	summarizerBroken   bool
	// window is the context window the threshold was derived from. The
	// reactive free-prune stage compares against it (the limit the provider
	// enforces), not against the 70% proactive threshold — a pruned history
	// between the two would still succeed on retry.
	window int
	// mainModel is the model the run currently streams on; the summarizer
	// choice is made against it and remade after a mid-run switch.
	mainModel string
}

// compactPhase is the explicit phase of a run's compaction lifecycle.
// Twin fields lowWaterMark/reactiveAttempted/enabled move only via
// transition, keyed by this phase, so every path updates them together.
//
//	disabled --windowUpdate--> below (run born opt-out CAN be enabled here;
//	                            kept as characterization: product decision pending)
//	below --pruneFits/paidShrink--> pruned / paid
//	below --reactivePruneFits--> recovered (budget intact)
//	below --reactivePaidShrink/Fail--> spent (budget consumed, sticky)
//	spent --reactivePaid*--> REFUSED (free prune still allowed)
type compactPhase int

const (
	phaseDisabled compactPhase = iota
	phaseBelow
	phasePruned
	phasePaid
	phaseRecovered
	phaseSpent
)

// String names the compaction phase for reports and debugging.
func (p compactPhase) String() string {
	switch p {
	case phaseBelow:
		return "below"
	case phasePruned:
		return "pruned"
	case phasePaid:
		return "paid"
	case phaseRecovered:
		return "recovered"
	case phaseSpent:
		return "spent"
	default:
		return "disabled"
	}
}

// compactEventKind is the set of compaction decisions the loop can report.
type compactEventKind int

const (
	evBelowThreshold compactEventKind = iota
	evPruneFits
	evPaidShrink
	evPaidNoShrink
	evPaidError
	evReactivePruneFits
	evReactivePaidShrink
	evReactivePaidFail
	evReactiveNoop
	evWindowUpdate
)

// String names the compaction event for reports and debugging.
func (k compactEventKind) String() string {
	switch k {
	case evBelowThreshold:
		return "below-threshold"
	case evPruneFits:
		return "prune-fits"
	case evPaidShrink:
		return "paid-shrink"
	case evPaidNoShrink:
		return "paid-no-shrink"
	case evPaidError:
		return "paid-error"
	case evReactivePruneFits:
		return "reactive-prune-fits"
	case evReactivePaidShrink:
		return "reactive-paid-shrink"
	case evReactivePaidFail:
		return "reactive-paid-fail"
	case evReactiveNoop:
		return "reactive-noop"
	case evWindowUpdate:
		return "window-update"
	default:
		return "unknown"
	}
}

// compactEvent describes one compaction decision: its kind, the measured size
// to store as watermark (when the row says so), whether a free prune already
// rewrote the history (drives conditional notify), and the window carried by
// evWindowUpdate.
type compactEvent struct {
	kind    compactEventKind
	size    int
	rewrote bool
	window  int
}

// compactTransitionKey is one legal (phase, event) cell of the machine.
type compactTransitionKey struct {
	phase compactPhase
	event compactEventKind
}

// compactTransition is the effect of one legal cell: the next phase and which
// of the twin fields move. notify fires the compaction hook; notifyIfRewrote
// fires it only when the event already rewrote the history.
type compactTransition struct {
	next            compactPhase
	setWatermark    bool
	clearWatermark  bool
	consumeBudget   bool
	enable          bool
	notify          bool
	notifyIfRewrote bool
}

// compactTransitionTable is the ONLY legal phase x event mapping. A cell
// missing here is an illegal transition: transition refuses it with no field
// changes and no notify — never a panic in production.
var compactTransitionTable = map[compactTransitionKey]compactTransition{
	{phaseBelow, evBelowThreshold}:     {next: phaseBelow},
	{phasePruned, evBelowThreshold}:    {next: phasePruned},
	{phasePaid, evBelowThreshold}:      {next: phasePaid},
	{phaseRecovered, evBelowThreshold}: {next: phaseRecovered},
	{phaseSpent, evBelowThreshold}:     {next: phaseSpent},

	{phaseBelow, evPruneFits}:     {next: phasePruned, setWatermark: true, notify: true},
	{phasePruned, evPruneFits}:    {next: phasePruned, setWatermark: true, notify: true},
	{phasePaid, evPruneFits}:      {next: phasePruned, setWatermark: true, notify: true},
	{phaseRecovered, evPruneFits}: {next: phasePruned, setWatermark: true, notify: true},
	{phaseSpent, evPruneFits}:     {next: phasePruned, setWatermark: true, notify: true},

	{phaseBelow, evPaidShrink}:     {next: phasePaid, setWatermark: true, notify: true},
	{phasePruned, evPaidShrink}:    {next: phasePaid, setWatermark: true, notify: true},
	{phasePaid, evPaidShrink}:      {next: phasePaid, setWatermark: true, notify: true},
	{phaseRecovered, evPaidShrink}: {next: phasePaid, setWatermark: true, notify: true},
	{phaseSpent, evPaidShrink}:     {next: phasePaid, setWatermark: true, notify: true},

	{phaseBelow, evPaidNoShrink}:     {next: phasePaid, setWatermark: true, notifyIfRewrote: true},
	{phasePruned, evPaidNoShrink}:    {next: phasePaid, setWatermark: true, notifyIfRewrote: true},
	{phasePaid, evPaidNoShrink}:      {next: phasePaid, setWatermark: true, notifyIfRewrote: true},
	{phaseRecovered, evPaidNoShrink}: {next: phasePaid, setWatermark: true, notifyIfRewrote: true},
	{phaseSpent, evPaidNoShrink}:     {next: phasePaid, setWatermark: true, notifyIfRewrote: true},

	{phaseBelow, evPaidError}:     {next: phaseBelow, notifyIfRewrote: true},
	{phasePruned, evPaidError}:    {next: phasePruned, notifyIfRewrote: true},
	{phasePaid, evPaidError}:      {next: phasePaid, notifyIfRewrote: true},
	{phaseRecovered, evPaidError}: {next: phaseRecovered, notifyIfRewrote: true},
	{phaseSpent, evPaidError}:     {next: phaseSpent, notifyIfRewrote: true},

	{phaseBelow, evReactivePruneFits}:     {next: phaseRecovered, setWatermark: true, notify: true},
	{phasePruned, evReactivePruneFits}:    {next: phaseRecovered, setWatermark: true, notify: true},
	{phasePaid, evReactivePruneFits}:      {next: phaseRecovered, setWatermark: true, notify: true},
	{phaseRecovered, evReactivePruneFits}: {next: phaseRecovered, setWatermark: true, notify: true},
	{phaseSpent, evReactivePruneFits}:     {next: phaseRecovered, setWatermark: true, notify: true},

	{phaseBelow, evReactivePaidShrink}:     {next: phaseSpent, setWatermark: true, consumeBudget: true, notify: true},
	{phasePruned, evReactivePaidShrink}:    {next: phaseSpent, setWatermark: true, consumeBudget: true, notify: true},
	{phasePaid, evReactivePaidShrink}:      {next: phaseSpent, setWatermark: true, consumeBudget: true, notify: true},
	{phaseRecovered, evReactivePaidShrink}: {next: phaseSpent, setWatermark: true, consumeBudget: true, notify: true},

	{phaseBelow, evReactivePaidFail}:     {next: phaseSpent, consumeBudget: true, notifyIfRewrote: true},
	{phasePruned, evReactivePaidFail}:    {next: phaseSpent, consumeBudget: true, notifyIfRewrote: true},
	{phasePaid, evReactivePaidFail}:      {next: phaseSpent, consumeBudget: true, notifyIfRewrote: true},
	{phaseRecovered, evReactivePaidFail}: {next: phaseSpent, consumeBudget: true, notifyIfRewrote: true},

	{phaseBelow, evReactiveNoop}:     {next: phaseBelow},
	{phasePruned, evReactiveNoop}:    {next: phasePruned},
	{phasePaid, evReactiveNoop}:      {next: phasePaid},
	{phaseRecovered, evReactiveNoop}: {next: phaseRecovered},
	{phaseSpent, evReactiveNoop}:     {next: phaseSpent},

	{phaseDisabled, evWindowUpdate}:  {next: phaseBelow, enable: true, clearWatermark: true},
	{phaseBelow, evWindowUpdate}:     {next: phaseBelow, enable: true, clearWatermark: true},
	{phasePruned, evWindowUpdate}:    {next: phaseBelow, enable: true, clearWatermark: true},
	{phasePaid, evWindowUpdate}:      {next: phaseBelow, enable: true, clearWatermark: true},
	{phaseRecovered, evWindowUpdate}: {next: phaseBelow, enable: true, clearWatermark: true},
	{phaseSpent, evWindowUpdate}:     {next: phaseBelow, enable: true, clearWatermark: true},
}

// compactLegal reports whether (phase, event) is a legal cell of the machine.
func compactLegal(phase compactPhase, event compactEventKind) bool {
	_, ok := compactTransitionTable[compactTransitionKey{phase: phase, event: event}]
	return ok
}

// transition is the SOLE writer of lowWaterMark, reactiveAttempted and
// enabled (plus the window/threshold twins set alongside them). Callers
// describe what happened as a compactEvent; the table decides the next phase
// and the field updates, and the return value tells the caller whether to
// fire the compaction hook. Illegal cells are refused with no field changes
// and no notify.
func (state *compactionState) transition(ev compactEvent) (notify bool) {
	key := compactTransitionKey{phase: state.phase, event: ev.kind}
	if !compactLegal(state.phase, ev.kind) {
		return false
	}
	row := compactTransitionTable[key]
	if row.enable {
		state.enabled = true
		state.window = ev.window
		state.threshold = autoCompactThreshold(ev.window, state.limit)
	}
	if row.clearWatermark {
		state.lowWaterMark = 0
	}
	if row.setWatermark {
		state.lowWaterMark = ev.size
	}
	if row.consumeBudget {
		state.reactiveAttempted = true
	}
	state.phase = row.next
	if row.notify || (row.notifyIfRewrote && ev.rewrote) {
		return true
	}
	return false
}

// measuredSize is the single token measure used where the code already
// compared calibrated(estimate + tool-defs): the proactive threshold,
// watermark and shrink checks, and the reactive prune-fit size and
// low-water mark. The reactive no-op shrink check below deliberately stays
// on the RAW estimateTokens comparison: it agrees with measuredSize at every
// realistic margin and differs only by int() truncation at a 1-token edge,
// so the raw form is kept for exact parity (see
// TestRecoverOneTokenEdgeUsesRawVerdict).
func (state *compactionState) measuredSize(messages []qgruntime.Message, tools []qgruntime.ToolDefinition) int {
	return state.calibratedTokens(estimateTokens(messages) + estimateToolDefTokens(tools))
}

// calibrate folds one turn's (rawEstimate, actualPromptTokens) sample into the
// running calibration ratio. A single sample is clamped to a sane band so an
// outlier (a huge cache-read turn, a provider-overhead spike) can't skew the
// estimate enough to disable or thrash compaction.
func (state *compactionState) calibrate(rawEstimate int, actualPromptTokens int) {
	if !state.enabled || rawEstimate <= 0 || actualPromptTokens <= 0 {
		return
	}
	sample := float64(actualPromptTokens) / float64(rawEstimate)
	if sample < 0.5 {
		sample = 0.5
	} else if sample > 2.0 {
		sample = 2.0
	}
	if state.calibrationRatio <= 0 {
		state.calibrationRatio = 1.0
	}
	const alpha = 0.3 // weight on the newest sample; smooths jitter across turns
	state.calibrationRatio = state.calibrationRatio*(1-alpha) + sample*alpha
}

// calibratedTokens applies the learned ratio to a raw estimate. Before any sample
// arrives (ratio unset) it returns the raw estimate unchanged.
func (state *compactionState) calibratedTokens(raw int) int {
	if state.calibrationRatio <= 0 {
		return raw
	}
	return int(float64(raw) * state.calibrationRatio)
}

func newCompactionState(options Options, task *taskState) *compactionState {
	phase := phaseBelow
	if options.ContextWindow <= 0 {
		phase = phaseDisabled
	}
	state := &compactionState{
		enabled:           options.ContextWindow > 0,
		threshold:         autoCompactThreshold(options.ContextWindow, options.AutoCompactLimit),
		limit:             options.AutoCompactLimit,
		window:            options.ContextWindow,
		mainModel:         options.Model,
		preserveLast:      options.CompactionPreserveLast,
		phase:             phase,
		onUsage:           tagSummarizerUsage(options.OnUsage),
		task:              task,
		planner:           newContextPlanner(contextPlannerConfig{contextWindow: options.ContextWindow}),
		trace:             options.Trace,
		summarizerFactory: options.Summarizer,
		onCompacted:       options.OnCompaction,
	}
	return state
}

// tagSummarizerUsage marks usage flowing out of the compaction summarizer so
// surfaces can keep its cost accounting while skipping it for cache-prefix
// attribution. Nil onUsage stays nil.
func tagSummarizerUsage(onUsage func(Usage)) func(Usage) {
	if onUsage == nil {
		return nil
	}
	return func(usage Usage) {
		usage.Summarizer = true
		onUsage(usage)
	}
}

// notifyCompacted reports a history rewrite to whoever is listening. Only a
// real rewrite fires: no-op compactions leave the wire — and the next call's
// cache explanation — untouched.
func (state *compactionState) notifyCompacted() {
	if state == nil || state.onCompacted == nil {
		return
	}
	state.onCompacted()
}

// updateWindow re-derives the compaction threshold after a mid-run model
// switch. The low-water mark is reset: it was measured against the old
// threshold, and keeping it could suppress a compaction the smaller window
// now requires. <= 0 (unknown model) leaves everything unchanged.
// NOTE: on a run born with ContextWindow==0 this enables compaction;
// characterization pins that behavior and the product decision is pending.
func (state *compactionState) updateWindow(window int) {
	if window <= 0 {
		return
	}
	state.transition(compactEvent{kind: evWindowUpdate, window: window})
	if state.planner != nil {
		state.planner.config.contextWindow = window
	}
}

// switchModel records a mid-run model switch: the window is re-derived
// (window <= 0 keeps the old one) and the summarizer choice is forgotten so
// the next compaction re-resolves it against the new main model. A run that
// started on the cheap model (no dedicated summarizer) and escalated to an
// expensive one therefore regains cheap summaries — the exact workflow a
// dedicated summarizer exists for.
func (state *compactionState) switchModel(modelID string, window int) {
	state.mainModel = modelID
	state.updateWindow(window)
	state.summarizerProvider = nil
	state.summarizerResolved = false
	state.summarizerBroken = false
}

// summarizeProvider returns the provider summarization calls should use and
// whether it is the dedicated summarizer: that one when it applies to the
// current main model and is healthy, else the main provider. The factory runs
// at most once per main model; a build error marks the summarizer broken for
// that model. Callers branch on the boolean rather than comparing Provider
// values — interface comparison panics on a non-comparable dynamic type.
func (state *compactionState) summarizeProvider(ctx context.Context, main Provider) (Provider, bool) {
	if state.summarizerFactory == nil || state.summarizerBroken {
		return main, false
	}
	if !state.summarizerResolved {
		state.summarizerResolved = true
		built, err := state.summarizerFactory(ctx, state.mainModel)
		if err != nil {
			state.summarizerBroken = true
			return main, false
		}
		state.summarizerProvider = built
	}
	if state.summarizerProvider == nil {
		return main, false
	}
	return state.summarizerProvider, true
}

// fitsWindow reports whether a calibrated size is safely under the limit the
// provider enforces. The proactive threshold (70% of the window) is a trigger
// for the paid summarizer, not the hard limit; the reactive path compares
// against the window itself, with a small margin for estimate error, so a
// history pruned to, say, 85% of the window takes the free retry.
func (state *compactionState) fitsWindow(size int) bool {
	if state.window <= 0 {
		return size <= state.threshold
	}
	return size <= state.window-state.window/20
}

// maybeCompact runs proactive compaction at the top of a turn. It returns the
// (possibly compacted) message slice. It is a no-op when compaction is disabled,
// when the history is under threshold, or when the history has not grown past
// the low-water mark since the last compaction (the infinite-loop guard).
func (state *compactionState) maybeCompact(
	ctx context.Context,
	provider Provider,
	messages []qgruntime.Message,
	tools []qgruntime.ToolDefinition,
) []qgruntime.Message {
	if !state.enabled || state.limit < 0 {
		return messages
	}
	// Tool definitions are part of every request's input, so every size below
	// is a measuredSize (messages + tool defs, calibrated): the threshold
	// check, the watermark guard and the shrink checks all judge the same
	// domain.
	size := state.measuredSize(messages, tools)
	if size <= state.threshold {
		state.transition(compactEvent{kind: evBelowThreshold})
		return messages
	}
	// Only compact when the history has grown past where we last left it. This
	// stops the loop from re-summarizing an already-compacted history every turn
	// when it sits just over the threshold.
	if state.lowWaterMark > 0 && size <= state.lowWaterMark {
		state.transition(compactEvent{kind: evBelowThreshold})
		return messages
	}

	// CHEAP FIRST STAGE: reclaim context at qg token/latency cost by pruning
	// the bodies of old, large tool results (the model has already acted on
	// them). If that brings us back under threshold, skip the paid summarizer
	// entirely and preserve recent turns verbatim.
	rewrote := false
	if pruned, reclaimed := pruneSupersededReadResults(messages); reclaimed > 0 {
		messages = pruned
		rewrote = true
		size = state.measuredSize(messages, tools)
		if size <= state.threshold {
			if state.transition(compactEvent{kind: evPruneFits, size: size}) {
				state.notifyCompacted()
			}
			return messages
		}
	}
	if pruned, reclaimed := pruneStaleToolOutput(messages, state.preserveLast); reclaimed > 0 {
		messages = pruned
		rewrote = true
		size = state.measuredSize(messages, tools)
		if size <= state.threshold {
			if state.transition(compactEvent{kind: evPruneFits, size: size}) {
				state.notifyCompacted()
			}
			return messages
		}
	}

	compacted, err := Compact(messages, CompactionOptions{
		PreserveLast: state.preserveLast,
		Summarize:    state.summarizeClosure(ctx, provider),
		taskState:    state.task.snapshotForCompaction(messages),
	})
	if err != nil {
		// Summarizer failed: keep whatever the prune stage already reclaimed
		// (the reactive path or a later turn can try again); the hook still
		// fires when the history differs from what came in.
		if state.transition(compactEvent{kind: evPaidError, rewrote: rewrote}) {
			state.notifyCompacted()
		}
		return messages
	}
	newSize := state.measuredSize(compacted, tools)
	if newSize >= size {
		// Compaction did not actually shrink anything (e.g. nothing to
		// summarize). Leave the history untouched and don't churn next turn.
		// A prune-only rewrite above still counts as a rewrite.
		if state.transition(compactEvent{kind: evPaidNoShrink, size: size, rewrote: rewrote}) {
			state.notifyCompacted()
		}
		return messages
	}
	// Only count a compaction when it actually shrank the history, so the
	// compaction counter reflects real context reductions rather than paid
	// no-ops that left the token budget untouched.
	if r := trace.FromContext(ctx); r != nil {
		r.Counter(trace.CounterCompactionCount, 1)
	}
	if state.transition(compactEvent{kind: evPaidShrink, size: newSize}) {
		state.notifyCompacted()
	}
	return compacted
}

// recover runs reactive compaction after a provider/stream error. It compacts
// at most once per run when the error looks like a context-limit error and the
// history can actually be shrunk. The returned booleans are (compacted, retried) and the
// error is non-nil only when compaction itself failed (so the loop should give
// up). When retried is false the caller keeps its original error.
func (state *compactionState) recover(
	ctx context.Context,
	provider Provider,
	messages []qgruntime.Message,
	tools []qgruntime.ToolDefinition,
	errorMessage string,
) (compacted []qgruntime.Message, retried bool, err error) {
	if !state.enabled {
		// Compaction disabled (ContextWindow==0): stay a strict no-op so a
		// context-limit error never triggers an unexpected summarization call.
		return messages, false, nil
	}
	if !isContextLimitError(errorMessage) {
		return messages, false, nil
	}

	// CHEAP FIRST STAGE (mirrors maybeCompact): a context-limit failure can
	// often be fixed for free by dropping superseded reads and pruning old
	// tool-result bodies. It runs regardless of the one-shot reactive budget
	// (it costs nothing and cannot loop: a second pass finds nothing left to
	// prune). The free retry is taken only when the pruned history fits the
	// window the provider enforces — otherwise a retry would fail the same
	// way, so fall through to the paid summarizer in this same call.
	prunedAny := false
	if pruned, reclaimed := pruneSupersededReadResults(messages); reclaimed > 0 {
		messages, prunedAny = pruned, true
	}
	if pruned, reclaimed := pruneStaleToolOutput(messages, state.preserveLast); reclaimed > 0 {
		messages, prunedAny = pruned, true
	}
	if prunedAny {
		size := state.measuredSize(messages, tools)
		if state.fitsWindow(size) {
			if state.transition(compactEvent{kind: evReactivePruneFits, size: size}) {
				state.notifyCompacted()
			}
			return messages, true, nil
		}
	}

	if state.reactiveAttempted {
		return messages, false, nil
	}

	result, compactErr := Compact(messages, CompactionOptions{
		PreserveLast: state.preserveLast,
		Summarize:    state.summarizeClosure(ctx, provider),
		taskState:    state.task.snapshotForCompaction(messages),
	})
	if compactErr != nil {
		// A genuine compaction attempt was made (and failed): the budget is spent
		// so the loop gives up rather than retrying a failing summarizer forever.
		// A prune-only rewrite above still counts: the history differs from
		// what came in.
		if state.transition(compactEvent{kind: evReactivePaidFail, rewrote: prunedAny}) {
			state.notifyCompacted()
		}
		return messages, true, compactErr
	}
	if estimateTokens(result) >= estimateTokens(messages) {
		// Kept RAW on purpose (exact parity): this agrees with the
		// calibrated+tools measure at every realistic margin and differs
		// only by int() truncation at a 1-token edge, where the raw verdict
		// (retry a real shrink) is the behavior every characterization pins.
		// Nothing to compact; the retry would just fail again. Signal "not
		// retried" so the caller surfaces the original context-limit error. Do NOT
		// consume the one-shot budget here: a no-op recover (history too small to
		// shrink) must not disable a later recovery once the history has grown.
		// The caller keeps its original history when retried is false, so a
		// prune-only rewrite above is discarded with it: stay silent instead
		// of reporting a rewrite no listener ever sees.
		state.transition(compactEvent{kind: evReactiveNoop})
		return messages, false, nil
	}
	// Success: a real compaction shrank the history and we will retry. Consume the
	// one-shot budget now so a provider that keeps returning context-limit errors
	// after a successful compaction can't loop forever. Store the low-water mark in
	// the SAME measured domain maybeCompact uses, so the proactive shrink-guard
	// compares like with like. Count it now that the shrink is confirmed, so the
	// counter mirrors maybeCompact's real-reduction policy.
	if r := trace.FromContext(ctx); r != nil {
		r.Counter(trace.CounterCompactionCount, 1)
	}
	if state.transition(compactEvent{kind: evReactivePaidShrink, size: state.measuredSize(result, tools)}) {
		state.notifyCompacted()
	}
	return result, true, nil
}

// summarizeClosure builds a Summarize function backed by a focused, tool-less
// provider call. The summary stream intentionally does NOT forward OnText (so
// compaction stays invisible on the user-facing surface), but it DOES forward
// OnUsage so the summarizer's token cost is still counted by usage/budgeting.
// The call goes to the dedicated (cheap) summarizer when one applies; if that
// call fails the summarizer is marked broken for the current model and the
// same slice is retried once on the main provider, so the caller sees at most
// one (main-provider-quality) error.
func (state *compactionState) summarizeClosure(ctx context.Context, provider Provider) func([]qgruntime.Message) (string, error) {
	if state.planner == nil {
		state.planner = newContextPlanner(contextPlannerConfig{})
	}
	return func(toSummarize []qgruntime.Message) (string, error) {
		chosen, dedicated := state.summarizeProvider(ctx, provider)
		summary, err := summarizeWithPlanner(ctx, chosen, toSummarize, state.onUsage, state.planner, state.trace)
		if err == nil || !dedicated {
			return summary, err
		}
		state.summarizerBroken = true
		return summarizeWithPlanner(ctx, provider, toSummarize, state.onUsage, state.planner, state.trace)
	}
}

func summarizeWithPlanner(ctx context.Context, provider Provider, messages []qgruntime.Message, onUsage func(Usage), planner *contextPlanner, recorder *trace.Recorder) (string, error) {
	summary, err := summarizeMessagesOnce(ctx, provider, messages, onUsage, planner, recorder)
	if err == nil {
		return summary, nil
	}
	if len(messages) < 2 || !isContextLimitError(err.Error()) {
		return "", err
	}

	mid := len(messages) / 2
	left, leftErr := summarizeWithPlanner(ctx, provider, messages[:mid], onUsage, planner, recorder)
	if leftErr != nil {
		return "", leftErr
	}
	right, rightErr := summarizeWithPlanner(ctx, provider, messages[mid:], onUsage, planner, recorder)
	if rightErr != nil {
		return "", rightErr
	}

	// Re-summarize the two partial summaries into ONE so the persisted summary
	// stays a single, further-summarizable unit — not an ever-growing concatenated
	// blob that a later compaction (which can't split a single message) would fail
	// on. If even the combined partials don't fit (extreme), fall back to the
	// joined text: still better than failing, and each half is already compacted.
	combined := strings.TrimSpace(left + "\n\n" + right)
	reduced, reduceErr := summarizeMessagesOnce(ctx, provider, []qgruntime.Message{
		{Role: qgruntime.MessageRoleUser, Content: combined},
	}, onUsage, planner, recorder)
	if reduceErr != nil {
		if isContextLimitError(reduceErr.Error()) {
			// Even the two combined partials don't fit (extreme): fall back to the
			// joined text — still better than failing, and each half is already
			// compacted.
			return combined, nil
		}
		// A non-context failure (auth/network/provider) must surface unchanged, per
		// this function's contract — don't mask it behind the joined fallback.
		return "", reduceErr
	}
	return reduced, nil
}

// CompactionSummaryRequestMessages builds the tool-less [system, user]
// summarizer request shared by automatic context-pressure compaction and
// manual session compaction. The conversation is rendered to plain text with
// clamped tool payloads (see renderTranscript) so the request always ends on
// a user message: some providers answer a raw history ending in an assistant
// tool call or a tool result with empty text when no tools are declared.
func CompactionSummaryRequestMessages(messages []qgruntime.Message) []qgruntime.Message {
	return []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: CompactionSummaryInstructions},
		{Role: qgruntime.MessageRoleUser, Content: "Summarize this conversation:\n\n" + renderTranscript(messages)},
	}
}

// summarizeMessagesOnce performs a single tool-less summarization call.
func summarizeMessagesOnce(ctx context.Context, provider Provider, messages []qgruntime.Message, onUsage func(Usage), planner *contextPlanner, recorder *trace.Recorder) (string, error) {
	requestMessages := CompactionSummaryRequestMessages(messages)
	parts := systemPromptParts{prompt: CompactionSummaryInstructions, baseInstructions: CompactionSummaryInstructions}
	plan := planner.planWithPromptParts(requestMessages, nil, "", parts)
	recordContextPlanTrace(recorder, plan)
	stream, err := provider.StreamCompletion(ctx, plan.Request)
	if err != nil {
		return "", err
	}
	// Forward OnUsage (token accounting) but not OnText (keep compaction invisible
	// to the user); a nil onUsage is a no-op.
	collected := qgruntime.CollectStreamWithOptions(ctx, stream, qgruntime.CollectOptions{OnUsage: onUsage})
	if collected.Error != "" {
		return "", errors.New(collected.Error)
	}
	summary := strings.TrimSpace(collected.Text)
	if summary == "" {
		return "", errors.New("summarizer returned no text")
	}
	return summary, nil
}

// summaryToolResultClamp bounds each tool-result body in the summarizer input.
// Tool outputs dominate the elided middle's bulk (build logs, file reads,
// search results), yet the summary instructions only need their gist — the
// model that ACTED on the full output already turned it into decisions the
// surrounding messages record. 2,000 chars (~500 tokens) keeps the useful
// head+tail of a result while cutting the summarizer input by 5-10x on
// tool-heavy sessions; the summarizer's own cost is the price being cut.
const summaryToolResultClamp = 2000

// summaryToolArgsClamp bounds each tool-call argument blob in the transcript:
// a write_file call carries the whole file body in its arguments.
const summaryToolArgsClamp = 500

// clampForSummary head+tail-truncates text for the summarizer transcript,
// snapping to rune boundaries so the clamp never splits a UTF-8 sequence.
func clampForSummary(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	head := max * 3 / 4
	tail := max - head
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	start := len(text) - tail
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[:head] + "\n…[" + strconv.Itoa(len(text)-head-(len(text)-start)) + " chars omitted for summarization]…\n" + text[start:]
}

// renderTranscript flattens messages into a plain-text transcript for the
// summarizer. Secret scrubbing already happened upstream at the tool boundary.
// Tool-result bodies and tool-call arguments are clamped (see the constants
// above): the summary needs what was done and decided, not the raw payloads.
func renderTranscript(messages []qgruntime.Message) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case qgruntime.MessageRoleAssistant:
			line := "assistant: " + message.Content
			if len(message.ToolCalls) > 0 {
				calls := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					calls = append(calls, call.Name+"("+clampForSummary(call.Arguments, summaryToolArgsClamp)+")")
				}
				line += "\n[tool calls: " + strings.Join(calls, "; ") + "]"
			}
			lines = append(lines, line)
		case qgruntime.MessageRoleTool:
			lines = append(lines, "tool result: "+clampForSummary(message.Content, summaryToolResultClamp))
		default:
			lines = append(lines, string(message.Role)+": "+message.Content)
		}
	}
	return strings.Join(lines, "\n\n")
}
