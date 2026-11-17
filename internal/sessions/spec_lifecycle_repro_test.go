package sessions

import (
	"testing"
	"time"
)

// Repro for the spec-lifecycle defect (commit A): the store accepts any
// SpecStatus transition. These two must FAIL on untouched dev (no error
// today) and pass once the explicit matrix lands.
func TestSpecLifecycleReproRejectedToApprovedIsRefused(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir(), Now: sequenceClock([]time.Time{
		time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 1, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 2, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 3, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 4, 0, time.UTC),
	})})
	draft, err := store.Create(CreateInput{SessionID: "spec_repro_rej", SessionKind: SessionKindSpecDraft})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusDraft}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusRejected}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusApproved}); err == nil {
		t.Fatal("rejected -> approved must be refused, got nil error")
	}
}

func TestSpecLifecycleReproApprovedWithImplToRejectedIsRefused(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir(), Now: sequenceClock([]time.Time{
		time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 1, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 2, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 3, 0, time.UTC),
		time.Date(2026, 6, 8, 12, 0, 4, 0, time.UTC),
	})})
	draft, err := store.Create(CreateInput{SessionID: "spec_repro_appr", SessionKind: SessionKindSpecDraft})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusDraft}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordSpec(draft.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p", SpecStatus: SpecStatusRejected}); err == nil {
		t.Fatal("approved with impl -> rejected must be refused, got nil error")
	}
}

// Characterization of current (pre-matrix) behavior. Passes before and after
// without edits: every legal production write lands with its mirror event.
func TestSpecLifecycleCharLegalWrites(t *testing.T) {
	newStore := func(t *testing.T) *Store {
		t.Helper()
		return NewStore(StoreOptions{RootDir: t.TempDir(), Now: sequenceClock([]time.Time{
			time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 1, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 2, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 3, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 4, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 5, 0, time.UTC),
			time.Date(2026, 6, 8, 12, 0, 6, 0, time.UTC),
		})})
	}
	t.Run("draft to approved with mirror event", func(t *testing.T) {
		store := newStore(t)
		s, err := store.Create(CreateInput{SessionID: "c1", SessionKind: SessionKindSpecDraft})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusDraft}); err != nil {
			t.Fatal(err)
		}
		updated, event, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-1"})
		if err != nil {
			t.Fatal(err)
		}
		if updated.SpecStatus != SpecStatusApproved || event.Type != EventSpecApproved {
			t.Fatalf("got status=%q event=%q", updated.SpecStatus, event.Type)
		}
	})
	t.Run("draft to rejected with mirror event", func(t *testing.T) {
		store := newStore(t)
		s, err := store.Create(CreateInput{SessionID: "c2", SessionKind: SessionKindSpecDraft})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusDraft}); err != nil {
			t.Fatal(err)
		}
		updated, event, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusRejected, SpecRejectReason: "too broad"})
		if err != nil {
			t.Fatal(err)
		}
		if updated.SpecStatus != SpecStatusRejected || event.Type != EventSpecRejected {
			t.Fatalf("got status=%q event=%q", updated.SpecStatus, event.Type)
		}
	})
	t.Run("draft to draft rewrites with mirror event", func(t *testing.T) {
		store := newStore(t)
		s, err := store.Create(CreateInput{SessionID: "c3", SessionKind: SessionKindSpecDraft})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusDraft}); err != nil {
			t.Fatal(err)
		}
		updated, event, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusDraft})
		if err != nil {
			t.Fatal(err)
		}
		if updated.SpecStatus != SpecStatusDraft || event.Type != EventSpecDraft {
			t.Fatalf("got status=%q event=%q", updated.SpecStatus, event.Type)
		}
	})
	t.Run("approved with impl re-approve stays approved", func(t *testing.T) {
		store := newStore(t)
		s, err := store.Create(CreateInput{SessionID: "c4", SessionKind: SessionKindSpecDraft})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusDraft}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-1"}); err != nil {
			t.Fatal(err)
		}
		updated, event, err := store.RecordSpec(s.SessionID, RecordSpecInput{SpecID: "s", SpecFilePath: "p.md", SpecStatus: SpecStatusApproved, SpecImplSessionID: "impl-1"})
		if err != nil {
			t.Fatal(err)
		}
		if updated.SpecStatus != SpecStatusApproved || updated.SpecImplSessionID != "impl-1" || event.Type != EventSpecApproved {
			t.Fatalf("got %#v event=%q", updated, event.Type)
		}
	})
}
