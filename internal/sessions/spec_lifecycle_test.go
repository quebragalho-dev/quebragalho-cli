package sessions

import (
	"strings"
	"testing"
)

// Table fixing the whole state x event space of the spec-lifecycle machine
// (commit B). Exactly the 8 production cells are legal; everything else is
// refused with an error and no state change (never a panic).
func TestSpecTransitionTable(t *testing.T) {
	allFrom := []specPhase{specEmpty, specDraftPhase, specApprovedPhase, specRejectedPhase}
	allTo := []specPhase{specDraftPhase, specApprovedPhase, specRejectedPhase}
	legal := map[specTransitionKey]bool{}
	for k := range specTransitionTable {
		legal[k] = true
	}
	wantLegal := map[specTransitionKey]bool{
		{specEmpty, specDraftPhase, false}:            true,
		{specDraftPhase, specDraftPhase, false}:       true,
		{specDraftPhase, specApprovedPhase, false}:    true,
		{specDraftPhase, specRejectedPhase, false}:    true,
		{specApprovedPhase, specApprovedPhase, false}: true,
		{specApprovedPhase, specApprovedPhase, true}:  true,
		{specApprovedPhase, specRejectedPhase, false}: true,
		{specRejectedPhase, specRejectedPhase, false}: true,
	}
	for _, from := range allFrom {
		for _, to := range allTo {
			for _, hasImpl := range []bool{false, true} {
				key := specTransitionKey{from: from, to: to, hasImpl: hasImpl}
				got := specLegal(from, to, hasImpl)
				want := wantLegal[key]
				if got != want {
					t.Errorf("specLegal(%s -> %s, hasImpl=%v) = %v, want %v", from, to, hasImpl, got, want)
				}
				// Table is the single source: every legal cell is in the map.
				if want && !legal[key] {
					t.Errorf("legal cell missing from table: %+v", key)
				}
				if !want && legal[key] {
					t.Errorf("illegal cell present in table: %+v", key)
				}
			}
		}
	}
	// Out-of-range phases are refused, never panic.
	if specLegal(specPhase(99), specDraftPhase, false) {
		t.Error("out-of-range from must be illegal")
	}
	if specLegal(specEmpty, specPhase(99), false) {
		t.Error("out-of-range to must be illegal")
	}
	if got := specEmpty.String(); got != "empty" {
		t.Errorf("String() = %q, want empty", got)
	}
	if got := SpecStatusDraft.String(); got != "draft" {
		t.Errorf("SpecStatus String() = %q, want draft", got)
	}
}

// Illegal transitions are refused with no state change and no panic.
func TestSpecTransitionRefusesWithoutMutating(t *testing.T) {
	session := Metadata{SessionID: "s", SessionKind: SessionKindSpecDraft, SpecStatus: SpecStatusRejected}
	before := session
	if _, err := transitionSpecRecord(&session, RecordSpecInput{SpecStatus: SpecStatusApproved}, SpecStatusApproved); err == nil {
		t.Fatal("rejected -> approved must fail")
	} else if !strings.Contains(err.Error(), "illegal transition") {
		t.Fatalf("error = %q, want illegal transition", err)
	}
	if session != before {
		t.Fatalf("illegal transition mutated state: %#v", session)
	}
	session2 := Metadata{SessionID: "s", SessionKind: SessionKindSpecDraft, SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-1"}
	before2 := session2
	if _, err := transitionSpecRecord(&session2, RecordSpecInput{SpecStatus: SpecStatusRejected}, SpecStatusRejected); err == nil {
		t.Fatal("approved+impl -> rejected must fail")
	}
	if session2 != before2 {
		t.Fatalf("illegal transition mutated state: %#v", session2)
	}
}

// Twin fields move together: status, impl id and mirror event are fixed in one
// place by transitionSpecRecord.
func TestSpecTransitionFixesTwinsTogether(t *testing.T) {
	session := Metadata{SessionID: "s", SessionKind: SessionKindSpecDraft, SpecStatus: SpecStatusDraft}
	event, err := transitionSpecRecord(&session, RecordSpecInput{SpecID: "id", SpecFilePath: "p", SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-9"}, SpecStatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	if session.SpecStatus != SpecStatusApproved || session.SpecImplSessionID != "impl-9" {
		t.Fatalf("twins not fixed together: %#v", session)
	}
	if event != EventSpecApproved {
		t.Fatalf("event = %q, want approved", event)
	}
}
