package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// SM3 Machine 1, commit B: table tests fixing the whole state x event space
// of the checkpoint/rewind machine. kindOf never changes the JSON encoding
// (asserted structurally: no new fields on CheckpointFile).

// All 8 Blob x Absent x Skipped combinations map to exactly one kind; only
// the three single-flag shapes are valid.
func TestSm3CheckpointKindTable(t *testing.T) {
	const hash = "abc123"
	cases := []struct {
		file CheckpointFile
		want checkpointKind
	}{
		{CheckpointFile{Path: "x"}, checkpointInvalid},
		{CheckpointFile{Path: "x", Blob: hash}, checkpointBlob},
		{CheckpointFile{Path: "x", Absent: true}, checkpointAbsent},
		{CheckpointFile{Path: "x", Skipped: true}, checkpointSkipped},
		{CheckpointFile{Path: "x", Blob: hash, Absent: true}, checkpointInvalid},
		{CheckpointFile{Path: "x", Blob: hash, Skipped: true}, checkpointInvalid},
		{CheckpointFile{Path: "x", Absent: true, Skipped: true}, checkpointInvalid},
		{CheckpointFile{Path: "x", Blob: hash, Absent: true, Skipped: true}, checkpointInvalid},
	}
	for _, tc := range cases {
		if got := tc.file.kindOf(); got != tc.want {
			t.Errorf("kindOf(%+v) = %s, want %s", tc.file, got, tc.want)
		}
	}
}

// Every kind (plus out-of-range values) maps to exactly one action; illegal
// kinds are refused as report-invalid and never panic.
func TestSm3CheckpointTransitionTable(t *testing.T) {
	cases := []struct {
		kind checkpointKind
		want restoreAction
	}{
		{checkpointBlob, actionRestoreBlob},
		{checkpointAbsent, actionDeleteAbsent},
		{checkpointSkipped, actionSkip},
		{checkpointInvalid, actionReportInvalid},
		{checkpointKind(-1), actionReportInvalid},
		{checkpointKind(99), actionReportInvalid},
	}
	for _, tc := range cases {
		if got := transitionKind(tc.kind); got != tc.want {
			t.Errorf("transitionKind(%d) = %s, want %s", int(tc.kind), got, tc.want)
		}
		if _, action := transitionCheckpointFile(CheckpointFile{Path: "x", Blob: "h"}); tc.kind == checkpointBlob && action != tc.want {
			t.Errorf("transitionCheckpointFile(blob) = %s, want %s", action, tc.want)
		}
	}
	if got := checkpointInvalid.String(); got != "invalid" {
		t.Errorf("String() = %q, want invalid", got)
	}
	if got := actionReportInvalid.String(); got != "report-invalid" {
		t.Errorf("String() = %q, want report-invalid", got)
	}
}

// Combined-flag entries are invalid: reported, never restored nor deleted.
func TestSm3RestoreReportsCombinedFlagEntries(t *testing.T) {
	store, ws := newCkStore(t)
	target, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.writeBlob("s", []byte("attacker"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, "mix.txt")
	mustWriteFile(t, path, "current")
	if _, err := store.AppendEvent("s", AppendEventInput{
		Type:    EventSessionCheckpoint,
		Payload: CheckpointPayload{Tool: "write_file", Files: []CheckpointFile{{Path: "mix.txt", Blob: hash, Absent: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := store.RestoreToSequence("s", ws, target.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesRestored != 0 || report.FilesDeleted != 0 {
		t.Fatalf("combined-flag entry must never restore nor delete, got %+v", report)
	}
	found := false
	for _, skipped := range report.Skipped {
		if skipped == "mix.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("combined-flag entry must be reported, got %+v", report)
	}
	if got, _ := os.ReadFile(path); string(got) != "current" {
		t.Fatalf("combined-flag entry must leave the file untouched, got %q", got)
	}
}

// The whole truncate head space: contiguous, gapped, empty and fully-cut
// logs map to one (EventCount, LastEventType) head.
func TestSm3DeriveTruncateHeadTable(t *testing.T) {
	msg := func(seq int) Event { return Event{Sequence: seq, Type: EventMessage} }
	chk := func(seq int) Event { return Event{Sequence: seq, Type: EventSessionCheckpoint} }
	res := func(seq int) Event { return Event{Sequence: seq, Type: EventToolResult} }
	cases := []struct {
		name   string
		events []Event
		keep   int
		count  int
		typ    EventType
	}{
		{"empty log", nil, 5, 0, ""},
		{"contiguous keep all", []Event{msg(1), chk(2), res(3)}, 3, 3, EventToolResult},
		{"contiguous keep head", []Event{msg(1), chk(2), res(3)}, 1, 1, EventMessage},
		{"contiguous keep none", []Event{msg(1), chk(2)}, 0, 0, ""},
		{"gapped keep all", []Event{msg(1), msg(2), msg(6)}, 6, 6, EventMessage},
		{"gapped cut at gap", []Event{msg(1), msg(2), msg(6)}, 4, 2, EventMessage},
		{"gapped keep none", []Event{msg(4), msg(6)}, 3, 0, ""},
		{"last kept type wins", []Event{msg(1), res(2), chk(5)}, 5, 5, EventSessionCheckpoint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head := deriveTruncateHead(tc.events, tc.keep)
			if head.eventCount != tc.count || head.lastEventType != tc.typ {
				t.Fatalf("deriveTruncateHead = (%d, %q), want (%d, %q)",
					head.eventCount, head.lastEventType, tc.count, tc.typ)
			}
		})
	}
}
