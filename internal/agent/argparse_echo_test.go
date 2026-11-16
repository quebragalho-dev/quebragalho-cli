package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func malformedCallTurn(id string, payload string) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: id, ToolName: "read_file"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: id, ArgumentsFragment: payload},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: id},
		{Type: qgruntime.StreamEventDone},
	}
}

func parseErrorSeenByModel(t *testing.T, request qgruntime.CompletionRequest) string {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role == qgruntime.MessageRoleTool && strings.Contains(message.Content, "Failed to parse arguments for read_file") {
			return message.Content
		}
	}
	t.Fatalf("no parse error reached the model: %#v", request.Messages)
	return ""
}

// Pins that the model sees the raw arguments it failed to parse.
func TestRunShowsTheModelTheArgumentsItFailedToParse(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(t.TempDir(), nil))
	const payload = `{"path":"README.md","limit":`
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		malformedCallTurn("call-1", payload),
		{{Type: qgruntime.StreamEventText, Content: "recovered"}, {Type: qgruntime.StreamEventDone}},
	}}

	if _, err := Run(context.Background(), "read", provider, Options{Registry: registry, MaxTurns: 2}); err != nil {
		t.Fatal(err)
	}

	seen := parseErrorSeenByModel(t, provider.requests[1])
	if !strings.Contains(seen, payload) {
		t.Fatalf("the model must see the arguments it sent, got %q", seen)
	}
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleAssistant && len(message.ToolCalls) > 0 {
			if got := message.ToolCalls[0].Arguments; !json.Valid([]byte(got)) || got != "{}" {
				t.Fatalf("replayed call arguments must stay sanitized, got %q", got)
			}
		}
	}
}

// Pins that a huge malformed payload is echoed capped, keeping both ends.
func TestRunCapsTheEchoOfUnparseableArguments(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(t.TempDir(), nil))
	payload := `{"path":"HEAD-MARK` + strings.Repeat("x", 20000) + `TAIL-MARK`
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		malformedCallTurn("call-1", payload),
		{{Type: qgruntime.StreamEventText, Content: "recovered"}, {Type: qgruntime.StreamEventDone}},
	}}

	if _, err := Run(context.Background(), "read", provider, Options{Registry: registry, MaxTurns: 2}); err != nil {
		t.Fatal(err)
	}

	seen := parseErrorSeenByModel(t, provider.requests[1])
	if len(seen) > 1024 {
		t.Fatalf("the echo must be capped, got %d bytes", len(seen))
	}
	if !strings.Contains(seen, "HEAD-MARK") || !strings.Contains(seen, "TAIL-MARK") {
		t.Fatalf("the echo must keep both ends of the payload, got %q", seen)
	}
}

// Pins that the repeated-failure guard still stops the run when each payload differs.
func TestRunStillStopsWhenEveryMalformedPayloadDiffers(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(t.TempDir(), nil))
	turns := make([][]qgruntime.StreamEvent, 0, 10)
	for i := 0; i < 10; i++ {
		turns = append(turns, malformedCallTurn("call-"+strconv.Itoa(i), `[`+strconv.Itoa(i)+`,"README.md"`))
	}
	provider := &mockProvider{turns: turns}

	result, err := Run(context.Background(), "read", provider, Options{Registry: registry, MaxTurns: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != toolFailureStopAt {
		t.Fatalf("expected the repeated-failure stop at %d requests, made %d (answer %q)", toolFailureStopAt, len(provider.requests), result.FinalAnswer)
	}
}

// Pins that echoed payload bytes never leak into the repeated-failure signature.
func TestArgumentParseFailureKeepsOneSignatureAcrossPayloads(t *testing.T) {
	shortest := errors.New("unexpected end of JSON input")
	first := errorSignature(argumentParseFailure("x", shortest, `[1,"a"`))
	second := errorSignature(argumentParseFailure("x", shortest, `{"b":`))
	if first != second {
		t.Fatalf("the echoed payload leaked into the failure signature: %q vs %q", first, second)
	}
}
