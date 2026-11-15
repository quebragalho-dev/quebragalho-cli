package sessions

import (
	"errors"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

var errTestInvalid = errors.New("invalid continuation")

// Table fixing the whole verdict space (pure refactor). Every
// hasSnap x valid x fresh x goalMatch x nonEmpty cell maps to exactly one
// verdict written out below (not derived from production code); the TUI
// branches mirror these 32 expectations byte-for-byte:
// exact iff hasSnap && valid && fresh && goalMatch && nonEmpty,
// invalid iff !valid, else reconstruct.
func TestContinuationVerdictTable(t *testing.T) {
	cases := []struct {
		hasSnap   bool
		valid     bool
		fresh     bool
		goalMatch bool
		nonEmpty  bool
		want      ContVerdict
	}{
		{false, false, false, false, false, ContInvalid},
		{false, false, false, false, true, ContInvalid},
		{false, false, false, true, false, ContInvalid},
		{false, false, false, true, true, ContInvalid},
		{false, false, true, false, false, ContInvalid},
		{false, false, true, false, true, ContInvalid},
		{false, false, true, true, false, ContInvalid},
		{false, false, true, true, true, ContInvalid},
		{false, true, false, false, false, ContReconstruct},
		{false, true, false, false, true, ContReconstruct},
		{false, true, false, true, false, ContReconstruct},
		{false, true, false, true, true, ContReconstruct},
		{false, true, true, false, false, ContReconstruct},
		{false, true, true, false, true, ContReconstruct},
		{false, true, true, true, false, ContReconstruct},
		{false, true, true, true, true, ContReconstruct},
		{true, false, false, false, false, ContInvalid},
		{true, false, false, false, true, ContInvalid},
		{true, false, false, true, false, ContInvalid},
		{true, false, false, true, true, ContInvalid},
		{true, false, true, false, false, ContInvalid},
		{true, false, true, false, true, ContInvalid},
		{true, false, true, true, false, ContInvalid},
		{true, false, true, true, true, ContInvalid},
		{true, true, false, false, false, ContReconstruct},
		{true, true, false, false, true, ContReconstruct},
		{true, true, false, true, false, ContReconstruct},
		{true, true, false, true, true, ContReconstruct},
		{true, true, true, false, false, ContReconstruct},
		{true, true, true, false, true, ContReconstruct},
		{true, true, true, true, false, ContReconstruct},
		{true, true, true, true, true, ContExact},
	}
	if len(cases) != 32 {
		t.Fatalf("table must cover all 32 cells, got %d", len(cases))
	}
	for _, tc := range cases {
		got := decideContinuationVerdict(tc.hasSnap, tc.valid, tc.fresh, tc.goalMatch, tc.nonEmpty)
		if got != tc.want {
			t.Errorf("decide(%v %v %v %v %v) = %s, want %s", tc.hasSnap, tc.valid, tc.fresh, tc.goalMatch, tc.nonEmpty, got, tc.want)
		}
	}
	// Out-of-range verdicts stringify as invalid, never panic.
	if got := ContVerdict(99).String(); got != "invalid" {
		t.Errorf("String() = %q, want invalid", got)
	}
	if got := ContExact.String(); got != "exact" {
		t.Errorf("String() = %q, want exact", got)
	}
	if got := ContReconstruct.String(); got != "reconstruct" {
		t.Errorf("String() = %q, want reconstruct", got)
	}
	// Tolerated set is the single source: exactly these six keep freshness.
	for _, typ := range []EventType{EventUsage, EventError, EventPermission, EventPermissionRequest, EventPermissionDecision, EventSessionCheckpoint} {
		if !continuationToleratedEvents[typ] {
			t.Errorf("tolerated set missing %q", typ)
		}
	}
	for _, typ := range []EventType{EventMessage, EventToolCall, EventToolResult, EventCompaction, EventSessionRewind, EventSessionFork, EventGoalCreated, EventSpecApproved} {
		if continuationToleratedEvents[typ] {
			t.Errorf("tolerated set must not contain %q", typ)
		}
	}
}

// DecideResumeVerdict mirrors the TUI branches: invalid on read error,
// reconstruct on missing/stale/goal/empty, exact only on fresh+goal+nonEmpty.
func TestDecideResumeVerdictMirrorsTUI(t *testing.T) {
	mkSnap := func(n int) *ContinuationSnapshot {
		msgs := make([]qgruntime.Message, n)
		for i := range msgs {
			msgs[i] = qgruntime.Message{Role: qgruntime.MessageRoleUser, Content: "x"}
		}
		return &ContinuationSnapshot{SessionID: "s", Messages: msgs}
	}
	if got := DecideResumeVerdict(nil, errTestInvalid, true, true); got != ContInvalid {
		t.Errorf("read error must be invalid, got %s", got)
	}
	if got := DecideResumeVerdict(nil, nil, true, true); got != ContReconstruct {
		t.Errorf("missing snapshot must reconstruct, got %s", got)
	}
	if got := DecideResumeVerdict(mkSnap(1), nil, false, true); got != ContReconstruct {
		t.Errorf("stale must reconstruct, got %s", got)
	}
	if got := DecideResumeVerdict(mkSnap(1), nil, true, false); got != ContReconstruct {
		t.Errorf("goal mismatch must reconstruct, got %s", got)
	}
	if got := DecideResumeVerdict(mkSnap(0), nil, true, true); got != ContReconstruct {
		t.Errorf("empty wire must reconstruct, got %s", got)
	}
	if got := DecideResumeVerdict(mkSnap(2), nil, true, true); got != ContExact {
		t.Errorf("fresh+goal+nonEmpty must be exact, got %s", got)
	}
}
