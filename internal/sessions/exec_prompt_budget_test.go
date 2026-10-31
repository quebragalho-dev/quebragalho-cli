package sessions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func mustBudgetPayload(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func budgetPrompt(t *testing.T, events []Event) string {
	t.Helper()
	return FormatExecPrompt("continue", PreparedExec{
		Mode:          ModeResume,
		Session:       Metadata{SessionID: "budget"},
		ContextEvents: events,
	})
}

// The previous turn ended with a ~3KB answer whose question sits at the very
// end. Resuming with "Sim" must still see that question.
func TestBudgetLastAssistantAnswerSurvivesWhole(t *testing.T) {
	body := strings.Repeat("lorem ipsum dolor sit amet ", 110)
	question := "E no fim, voce quer que eu continue com a correcao? Responda Sim ou Nao."
	full := strings.TrimSpace(body) + " " + question
	if len(full) < 3000 {
		t.Fatalf("SETUP INVALID: answer is %d bytes, want ~3000", len(full))
	}
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "comece a tarefa"})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": full})},
	}
	prompt := budgetPrompt(t, events)
	if !strings.Contains(prompt, question) {
		t.Fatalf("last assistant question was cut from the resume prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, full) {
		t.Fatalf("last assistant answer is not whole in the prompt (%d bytes):\n%s", len(full), prompt)
	}
}

// Message lines name the role first, so a long event keeps its speaker even
// after the head+tail cut.
func TestBudgetRoleComesBeforeContent(t *testing.T) {
	long := "HEAD-MARKER-ABC123 " + strings.Repeat("x", 3000) + " TAIL-MARKER-XYZ789"
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": long})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": "ok"})},
		{Sequence: 3, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "next"})},
	}
	prompt := budgetPrompt(t, events)
	if !strings.Contains(prompt, "- #1 user: ") {
		t.Fatalf("message line does not start with role:\n%s", prompt)
	}
	if !strings.Contains(prompt, "TAIL-MARKER-XYZ789") {
		t.Fatalf("long event tail was dropped, role survival unproven:\n%s", prompt)
	}
}

// A content list carries text next to binary parts: only the text reaches the
// prompt, with no role duplication and no base64.
func TestBudgetMessagePartsKeepTextOnly(t *testing.T) {
	payload := mustBudgetPayload(t, map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "hello text"},
			map[string]any{"type": "image", "data": "QUJDQUJD", "mimeType": "image/png"},
		},
	})
	prompt := budgetPrompt(t, []Event{{Sequence: 1, Type: EventMessage, Payload: payload}})
	var line string
	for _, candidate := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(candidate, "- #1 ") {
			line = candidate
		}
	}
	if line != "- #1 user: hello text" {
		t.Fatalf("parts rendered as %q, want text only:\n%s", line, prompt)
	}
	if strings.Contains(prompt, "QUJD") {
		t.Fatalf("image base64 leaked into the prompt:\n%s", prompt)
	}
}

// A last answer over 24 KiB keeps head and tail with an omission marker.
func TestBudgetHugeLastAnswerKeepsHeadAndTail(t *testing.T) {
	head := "HEAD-KEEP-ME-111 "
	tail := " TAIL-KEEP-ME-222 pergunta final: devo prosseguir?"
	fillerA := strings.Repeat("a", 15000)
	fillerB := strings.Repeat("b", 15000)
	full := head + fillerA + fillerB + tail
	if len(full) <= 24*1024 {
		t.Fatalf("SETUP INVALID: answer is %d bytes, want over 24 KiB", len(full))
	}
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "go"})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": full})},
	}
	prompt := budgetPrompt(t, events)
	for _, want := range []string{"HEAD-KEEP-ME-111", "TAIL-KEEP-ME-222", "bytes omitted", "devo prosseguir?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("huge answer lost %q:\n%s", want, prompt)
		}
	}
}

// The block has a total budget: old events drop with one omission line while
// the session's first user request stays.
func TestBudgetTotalKeepsFirstAndOmitsOld(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "FIRST-USER-MARKER-keep-me"})},
	}
	for seq := 2; seq <= 30; seq++ {
		role := "user"
		if seq%2 == 0 {
			role = "assistant"
		}
		content := fmt.Sprintf("MSG-%02d ", seq) + strings.Repeat("x", 2000)
		events = append(events, Event{Sequence: seq, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": role, "content": content})})
	}
	prompt := budgetPrompt(t, events)
	if !strings.Contains(prompt, "FIRST-USER-MARKER-keep-me") {
		t.Fatalf("first user message was evicted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "events omitted") {
		t.Fatalf("over-budget context has no omission line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "MSG-30") {
		t.Fatalf("most recent event was evicted:\n%s", prompt)
	}
	total := 0
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "- #") && !strings.Contains(line, "events omitted") {
			total += len(line)
		}
	}
	if total > 48*1024 {
		t.Fatalf("context block is %d bytes, over the 48 KiB budget", total)
	}
}

// Events recorded before the first user message are counted in the leading
// gap: with the budget blown, the block stays chronological with the omission
// line between the pins and the suffix.
func TestBudgetPreFirstUserEventKeepsOrder(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": "PRE-MARKER-01"})},
		{Sequence: 2, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": "PRE-MARKER-02"})},
		{Sequence: 3, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "FIRST-USER-MARKER"})},
		{Sequence: 4, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": "LAST-ASSISTANT-MARKER"})},
	}
	for seq := 5; seq <= 28; seq++ {
		content := fmt.Sprintf("TAIL-%02d ", seq) + strings.Repeat("x", 3000)
		events = append(events, Event{Sequence: seq, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": content})})
	}
	prompt := budgetPrompt(t, events)
	for _, want := range []string{"FIRST-USER-MARKER", "LAST-ASSISTANT-MARKER", "TAIL-28"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("context lost %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "PRE-MARKER") {
		t.Fatalf("events before the first user message should be omitted:\n%s", prompt)
	}
	if count := strings.Count(prompt, "- … [2 events omitted] …"); count != 1 {
		t.Fatalf("expected one two-event leading omission line, got %d:\n%s", count, prompt)
	}
	if count := strings.Count(prompt, "- … [1 events omitted] …"); count != 1 {
		t.Fatalf("expected one single-event middle omission line, got %d:\n%s", count, prompt)
	}
	order := []string{"- … [2 events omitted] …", "- #3 ", "- #4 ", "- … [1 events omitted] …", "- #6 ", "- #28 "}
	remaining := prompt
	for _, marker := range order {
		index := strings.Index(remaining, marker)
		if index < 0 {
			t.Fatalf("marker %q missing or out of order:\n%s", marker, prompt)
		}
		remaining = remaining[index+len(marker):]
	}
}

// The last assistant answer survives a heavy interrupted tail: both pins are
// kept while the budget takes the most recent events first.
func TestBudgetLastAssistantPinnedUnderTailPressure(t *testing.T) {
	big := "LAST-ASSISTANT-MARKER " + strings.Repeat("y", 20*1024)
	if len(big) >= 24*1024 {
		t.Fatalf("SETUP INVALID: answer is %d bytes, want under 24 KiB whole", len(big))
	}
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "FIRST-USER-MARKER"})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": big})},
	}
	for seq := 3; seq <= 26; seq++ {
		content := fmt.Sprintf("TAIL-%02d ", seq) + strings.Repeat("x", 2000)
		events = append(events, Event{Sequence: seq, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": content})})
	}
	prompt := budgetPrompt(t, events)
	if !strings.Contains(prompt, "LAST-ASSISTANT-MARKER") {
		t.Fatalf("last assistant answer was evicted by its own tail:\n%s", prompt)
	}
	if !strings.Contains(prompt, "FIRST-USER-MARKER") {
		t.Fatalf("first user message was evicted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "events omitted") {
		t.Fatalf("over-budget context has no omission line:\n%s", prompt)
	}
}

// Past the first event that does not fit, nothing older appears: the kept
// tail is a contiguous suffix, so the single omission line tells the truth.
func TestBudgetSuffixStaysContiguous(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "FIRST-USER-MARKER"})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": "LAST-ASSISTANT-MARKER"})},
	}
	for seq := 3; seq <= 7; seq++ {
		content := fmt.Sprintf("SMALL-OLD-%02d", seq)
		events = append(events, Event{Sequence: seq, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": content})})
	}
	events = append(events, Event{Sequence: 8, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": "BIG-MARKER " + strings.Repeat("x", 2000)})})
	for seq := 9; seq <= 32; seq++ {
		content := fmt.Sprintf("MSG-%02d ", seq) + strings.Repeat("x", 2000)
		events = append(events, Event{Sequence: seq, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": content})})
	}
	prompt := budgetPrompt(t, events)
	if !strings.Contains(prompt, "FIRST-USER-MARKER") {
		t.Fatalf("first user message was evicted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "MSG-32") {
		t.Fatalf("most recent event was evicted:\n%s", prompt)
	}
	for _, unwanted := range []string{"SMALL-OLD", "BIG-MARKER"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("event older than the first unfitting one leaked into the prompt (%q):\n%s", unwanted, prompt)
		}
	}
}

// Each gap gets its own omission line with that gap's count: an event before
// the first user message and a break inside the tail are two gaps, not one.
func TestBudgetOmissionMarksEachGap(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": "PRE-MARKER"})},
		{Sequence: 2, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "user", "content": "FIRST-USER-MARKER"})},
		{Sequence: 3, Type: EventMessage, Payload: mustBudgetPayload(t, map[string]any{"role": "assistant", "content": "LAST-ASSISTANT-MARKER"})},
	}
	for seq := 4; seq <= 27; seq++ {
		content := fmt.Sprintf("TAIL-%02d ", seq) + strings.Repeat("x", 3000)
		events = append(events, Event{Sequence: seq, Type: EventError, Payload: mustBudgetPayload(t, map[string]any{"message": content})})
	}
	prompt := budgetPrompt(t, events)
	for _, want := range []string{"FIRST-USER-MARKER", "LAST-ASSISTANT-MARKER", "TAIL-27"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("context lost %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "PRE-MARKER") {
		t.Fatalf("event before the first user message should be omitted:\n%s", prompt)
	}
	if count := strings.Count(prompt, "- … [1 events omitted] …"); count != 2 {
		t.Fatalf("expected two single-event omission lines, got %d:\n%s", count, prompt)
	}
	order := []string{"- … [1 events omitted] …", "- #2 ", "- #3 ", "- … [1 events omitted] …", "- #5 "}
	positions := make([]int, 0, len(order))
	remaining := prompt
	offset := 0
	for _, marker := range order {
		index := strings.Index(remaining, marker)
		if index < 0 {
			t.Fatalf("marker %q missing or out of order:\n%s", marker, prompt)
		}
		positions = append(positions, offset+index)
		offset += index + len(marker)
		remaining = remaining[index+len(marker):]
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("context out of chronological order:\n%s", prompt)
		}
	}
}
