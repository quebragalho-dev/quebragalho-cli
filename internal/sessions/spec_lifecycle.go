package sessions

import (
	"fmt"
	"strings"
)

// Spec lifecycle is an explicit state machine. The store used to accept any
// SpecStatus transition and the matrix lived in CLI ifs; now RecordSpec
// consults one table up front and one transition owns every write.
//
//	draft --approve--> approved --re-approve--> approved (idempotent)
//	draft --reject---> rejected --re-reject---> rejected (idempotent)
//	draft --rewrite--> draft
//	""    --record---> draft (new draft session)
//	approved --reject--> rejected ONLY when no implementation exists
//	rejected --approve--> REFUSED (CLI already refuses; store closes the hole)
//	approved+impl --reject--> REFUSED (CLI already refuses; store closes it)
//	anything --record--> draft (back to draft) REFUSED
//
// Twin fields SpecStatus/SpecImplSessionID and the mirror event type move
// together in transitionSpecRecord only. SpecUserComment/SpecRejectReason are
// deliberately NOT cleared on transitions (byte-identical disk parity with
// 0.0.19): clearing them would change persisted JSON, so the two suspects
// about stale comment/reason stay described, not fixed.
type specPhase int

const (
	specEmpty specPhase = iota
	specDraftPhase
	specApprovedPhase
	specRejectedPhase
)

// String names the spec phase for reports and debugging.
func (p specPhase) String() string {
	switch p {
	case specDraftPhase:
		return "draft"
	case specApprovedPhase:
		return "approved"
	case specRejectedPhase:
		return "rejected"
	default:
		return "empty"
	}
}

// String names the spec status itself (string enum for reports).
func (s SpecStatus) String() string { return string(s) }

// specPhaseOf maps a stored status to its phase. Empty means no spec recorded
// yet (fresh draft session before the first RecordSpec).
func specPhaseOf(status SpecStatus) specPhase {
	switch status {
	case SpecStatusDraft:
		return specDraftPhase
	case SpecStatusApproved:
		return specApprovedPhase
	case SpecStatusRejected:
		return specRejectedPhase
	default:
		return specEmpty
	}
}

// specTransitionKey is one cell of the machine: current phase, requested
// phase, and whether the current record already carries an implementation.
type specTransitionKey struct {
	from    specPhase
	to      specPhase
	hasImpl bool
}

// specTransitionTable is the ONLY legal from x to x hasImpl mapping. A cell
// missing here is an illegal transition: transitionSpecRecord refuses it with
// an error and no field changes — never a panic in production. The table
// accepts exactly what production callers request today (exec --use-spec
// ""->draft; CLI/TUI draft->approved/rejected/draft; legacy approved->approved
// to attach an impl; approved->rejected only without an impl; idempotent
// re-approve and re-reject) and refuses only what none of them reaches
// (rejected->approved; approved+impl->rejected; any return to draft;
// empty->approved/rejected directly).
var specTransitionTable = map[specTransitionKey]bool{
	{specEmpty, specDraftPhase, false}:            true,
	{specDraftPhase, specDraftPhase, false}:       true,
	{specDraftPhase, specApprovedPhase, false}:    true,
	{specDraftPhase, specRejectedPhase, false}:    true,
	{specApprovedPhase, specApprovedPhase, false}: true,
	{specApprovedPhase, specApprovedPhase, true}:  true,
	{specApprovedPhase, specRejectedPhase, false}: true,
	{specRejectedPhase, specRejectedPhase, false}: true,
}

// specLegal reports whether (from, to, hasImpl) is a legal cell of the machine.
func specLegal(from, to specPhase, hasImpl bool) bool {
	return specTransitionTable[specTransitionKey{from: from, to: to, hasImpl: hasImpl}]
}

// transitionSpecRecord is the SOLE writer of the spec lifecycle fields
// (SpecStatus plus its twins SpecImplSessionID and the mirror event type).
// It validates (from, to, hasImpl) against specTransitionTable first; illegal
// transitions return an error with no field changes. Legal transitions apply
// the same conditional-overwrite writes applySpecRecord did (non-empty fields
// only), so persisted JSON stays byte-identical for every legal path.
func transitionSpecRecord(session *Metadata, input RecordSpecInput, status SpecStatus) (EventType, error) {
	from := specPhaseOf(session.SpecStatus)
	to := specPhaseOf(status)
	hasImpl := strings.TrimSpace(session.SpecImplSessionID) != ""
	if !specLegal(from, to, hasImpl) {
		return "", fmt.Errorf("qg spec illegal transition %q -> %q", from, to)
	}
	if specID := strings.TrimSpace(input.SpecID); specID != "" {
		session.SpecID = specID
	}
	if path := strings.TrimSpace(input.SpecFilePath); path != "" {
		session.SpecFilePath = path
	}
	session.SpecStatus = status
	if modelID := strings.TrimSpace(input.SpecDraftModelID); modelID != "" {
		session.SpecDraftModelID = modelID
	}
	if reasoning := strings.TrimSpace(input.SpecDraftReasoning); reasoning != "" {
		session.SpecDraftReasoning = reasoning
	}
	if comment := strings.TrimSpace(input.SpecUserComment); comment != "" {
		session.SpecUserComment = comment
	}
	if reason := strings.TrimSpace(input.SpecRejectReason); reason != "" {
		session.SpecRejectReason = reason
	}
	if sourceID := strings.TrimSpace(input.SpecSourceSessionID); sourceID != "" {
		session.SpecSourceSessionID = sourceID
	}
	if implID := strings.TrimSpace(input.SpecImplSessionID); implID != "" {
		session.SpecImplSessionID = implID
	}
	return specEventType(status), nil
}
