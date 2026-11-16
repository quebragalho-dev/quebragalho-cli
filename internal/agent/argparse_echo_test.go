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
	firstOutput, _ := argumentParseFailure("x", shortest, `[1,"a"`)
	secondOutput, _ := argumentParseFailure("x", shortest, `{"b":`)
	first, second := errorSignature(firstOutput), errorSignature(secondOutput)
	if first != second {
		t.Fatalf("the echoed payload leaked into the failure signature: %q vs %q", first, second)
	}
}

// Pins that the echoed payload crosses the same secret-scrubbing boundary as
// every other tool result, including a secret the clip would cut in half, and
// that the result is flagged as redacted.
func TestExecuteToolCallScrubsUnparseableArguments(t *testing.T) {
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	cases := map[string]string{
		"short payload": `{"path":"` + secret,
		// The echo keeps the first 288 bytes: the secret starts at byte 280, so a
		// clip taken before scrubbing would echo its first eight characters.
		"secret across the cut": `{"pad":"` + strings.Repeat("x", 261) + `","token":"` + secret + `","more":"` + strings.Repeat("y", 2000),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(tools.NewScopedReadFileTool(t.TempDir(), nil))

			result, err := executeToolCall(context.Background(), registry, ToolCall{ID: "call-1", Name: "read_file", Arguments: payload}, PermissionModeAuto, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Output, "Failed to parse arguments for read_file") {
				t.Fatalf("expected the parse failure, got %q", result.Output)
			}
			if strings.Contains(result.Output, "sk-proj-") {
				t.Fatalf("the echo leaked the secret or a prefix of it: %q", result.Output)
			}
			if !result.Redacted {
				t.Fatalf("a scrubbed echo must be flagged as redacted, output = %q", result.Output)
			}
		})
	}
}
