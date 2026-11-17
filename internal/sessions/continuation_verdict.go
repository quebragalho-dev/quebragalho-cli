package sessions

// Continuation resume verdict is an explicit short decision. Before, the
// exact-vs-reconstructed choice lived in TUI ifs (session.go) and the drift
// allowlist lived inline in CheckContinuationFresh; now one verdict enum,
// one short decision testable without TUI, one tolerated-event set, and one
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
// place by DecideResumeVerdict so TUI never re-implements the branches.
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

// decideContinuationVerdict is the SOLE decider of exact vs reconstructed vs
// invalid. Callers describe what they have (snapshot presence, read validity,
// freshness from CheckContinuationFresh, goal-key match, non-empty wire).
// Exact if and only if everything holds; invalid on unreadable input;
// otherwise reconstruct. Testable without TUI; TUI only maps the verdict
// to its existing warnings (byte-identical texts).
func decideContinuationVerdict(hasSnap, valid, fresh, goalMatch, nonEmpty bool) ContVerdict {
	switch {
	case !valid:
		return ContInvalid
	case !hasSnap || !fresh || !goalMatch || !nonEmpty:
		return ContReconstruct
	default:
		return ContExact
	}
}

// DecideResumeVerdict adapts the TUI resume inputs to the decision without
// touching disk or the TUI: snap may be nil, readErr reports
// ReadContinuation, fresh comes from CheckContinuationFresh, goalMatch
// compares snap.GoalKey, nonEmpty is len(snap.Messages) > 0.
func DecideResumeVerdict(snap *ContinuationSnapshot, readErr error, fresh bool, goalMatch bool) ContVerdict {
	hasSnap := snap != nil
	valid := readErr == nil
	nonEmpty := hasSnap && len(snap.Messages) > 0
	return decideContinuationVerdict(hasSnap, valid, fresh, goalMatch, nonEmpty)
}
