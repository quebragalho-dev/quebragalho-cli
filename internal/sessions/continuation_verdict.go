package sessions

// Continuation resume verdict is an explicit decision machine. Before, the
// exact-vs-reconstructed choice lived in TUI ifs (session.go) and the drift
// allowlist lived inline in CheckContinuationFresh; now one verdict enum,
// one decision table testable without TUI, one tolerated-event set, and one
// shared orphan-notice const decide everything. Pure refactor: no wire,
// message, or disk byte changes.
//
//	snapErr ------------------> invalid (unreadable file; reconstruct w/ warning)
//	snap==nil ----------------> reconstruct (old/fork session, no snapshot)
//	fresh==false -------------> reconstruct (rewind/stale delta)
//	goal mismatch ------------> reconstruct (goal changed)
//	empty wire ---------------> reconstruct (nothing to send exact)
//	fresh+goal+nonEmpty ------> exact (byte-identical wire)
//
// Twin fields (fresh, delta) move together in CheckContinuationFresh only,
// keyed by continuationToleratedEvents; (verdict, warning) is derived in one
// place by DecideResumeVerdict so TUI never re-implements the matrix.
type ContVerdict int

const (
	ContInvalid ContVerdict = iota
	ContExact
	ContReconstruct
)

// String names the continuation verdict for reports and debugging.
func (v ContVerdict) String() string {
	switch v {
	case ContExact:
		return "exact"
	case ContReconstruct:
		return "reconstruct"
	default:
		return "invalid"
	}
}

// continuationToleratedEvents is the ONLY list of delta event types that keep
// a snapshot fresh. Those events never enter the provider wire (usage,
// errors, permissions, checkpoint blobs for /rewind); any conversation,
// compaction, rewind, fork, goal or spec event after snap.EventSeq makes it
// stale. A kill landing between the snapshot and the checkpoint write stays
// exact because checkpoints are ignored here.
var continuationToleratedEvents = map[EventType]bool{
	EventUsage:              true,
	EventError:              true,
	EventPermission:         true,
	EventPermissionRequest:  true,
	EventPermissionDecision: true,
	EventSessionCheckpoint:  true,
}

// contDecisionKey is one cell of the verdict machine.
type contDecisionKey struct {
	hasSnap   bool
	valid     bool
	fresh     bool
	goalMatch bool
	nonEmpty  bool
}

// contVerdictTable is the ONLY input -> verdict mapping. Every combination is
// covered; there is no illegal cell (every input decides), and the function
// never panics. It mirrors the TUI resume branches byte-for-byte:
// read error -> invalid; missing snapshot -> reconstruct; stale/goal/empty ->
// reconstruct; otherwise exact.
var contVerdictTable = map[contDecisionKey]ContVerdict{
	{false, false, false, false, false}: ContInvalid,
	{false, false, false, false, true}:  ContInvalid,
	{false, false, false, true, false}:  ContInvalid,
	{false, false, false, true, true}:   ContInvalid,
	{false, false, true, false, false}:  ContInvalid,
	{false, false, true, false, true}:   ContInvalid,
	{false, false, true, true, false}:   ContInvalid,
	{false, false, true, true, true}:    ContInvalid,
	{false, true, false, false, false}:  ContReconstruct,
	{false, true, false, false, true}:   ContReconstruct,
	{false, true, false, true, false}:   ContReconstruct,
	{false, true, false, true, true}:    ContReconstruct,
	{false, true, true, false, false}:   ContReconstruct,
	{false, true, true, false, true}:    ContReconstruct,
	{false, true, true, true, false}:    ContReconstruct,
	{false, true, true, true, true}:     ContReconstruct,
	{true, false, false, false, false}:  ContInvalid,
	{true, false, false, false, true}:   ContInvalid,
	{true, false, false, true, false}:   ContInvalid,
	{true, false, false, true, true}:    ContInvalid,
	{true, false, true, false, false}:   ContInvalid,
	{true, false, true, false, true}:    ContInvalid,
	{true, false, true, true, false}:    ContInvalid,
	{true, false, true, true, true}:     ContInvalid,
	{true, true, false, false, false}:   ContReconstruct,
	{true, true, false, false, true}:    ContReconstruct,
	{true, true, false, true, false}:    ContReconstruct,
	{true, true, false, true, true}:     ContReconstruct,
	{true, true, true, false, false}:    ContReconstruct,
	{true, true, true, false, true}:     ContReconstruct,
	{true, true, true, true, false}:     ContReconstruct,
	{true, true, true, true, true}:      ContExact,
}

// decideContinuationVerdict is the SOLE decider of exact vs reconstructed vs
// invalid. Callers describe what they have (snapshot presence, read validity,
// freshness from CheckContinuationFresh, goal-key match, non-empty wire); the
// table returns the verdict. Testable without TUI; TUI only maps the verdict
// to its existing warnings (byte-identical texts).
func decideContinuationVerdict(hasSnap, valid, fresh, goalMatch, nonEmpty bool) ContVerdict {
	if verdict, ok := contVerdictTable[contDecisionKey{hasSnap: hasSnap, valid: valid, fresh: fresh, goalMatch: goalMatch, nonEmpty: nonEmpty}]; ok {
		return verdict
	}
	return ContReconstruct
}

// DecideResumeVerdict adapts the TUI resume inputs to the table without
// touching disk or the TUI: snap may be nil, readErr reports
// ReadContinuation, fresh comes from CheckContinuationFresh, goalMatch
// compares snap.GoalKey, nonEmpty is len(snap.Messages) > 0.
func DecideResumeVerdict(snap *ContinuationSnapshot, readErr error, fresh bool, goalMatch bool) ContVerdict {
	hasSnap := snap != nil
	valid := readErr == nil
	nonEmpty := hasSnap && len(snap.Messages) > 0
	return decideContinuationVerdict(hasSnap, valid, fresh, goalMatch, nonEmpty)
}
