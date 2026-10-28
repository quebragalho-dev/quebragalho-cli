package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// escalate_model is a control-only tool (SideEffectNone). The stream-json tool
// call must report sideEffect "none", not "unknown", so automation sees the
// promised value.
func TestStreamJSONSideEffectReportsNoneForControlTool(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewEscalateModelTool())
	if got := streamJSONSideEffect("escalate_model", registry); got != "none" {
		t.Fatalf("streamJSONSideEffect(escalate_model) = %q, want none", got)
	}
}

func TestExecWriterPropagatesToolResultTruncation(t *testing.T) {
	for _, format := range []execOutputFormat{execOutputJSON, execOutputStreamJSON} {
		t.Run(string(format), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			writer := execEventWriter{
				stdout:       &stdout,
				stderr:       &stderr,
				format:       format,
				runID:        "run_budget",
				streamedText: &strings.Builder{},
			}
			writer.toolResult(agent.ToolResult{
				ToolCallID: "call_budget",
				Name:       "read_file",
				Status:     tools.StatusOK,
				Output:     "bounded output",
				Truncated:  true,
			})
			if writer.err != nil {
				t.Fatalf("toolResult: %v", writer.err)
			}
			var payload map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
				t.Fatalf("decode output %q: %v", stdout.String(), err)
			}
			if payload["truncated"] != true {
				t.Fatalf("truncated = %#v, want true; payload=%#v", payload["truncated"], payload)
			}
		})
	}
}

func TestExecWriterPropagatesCacheUsage(t *testing.T) {
	for _, format := range []execOutputFormat{execOutputJSON, execOutputStreamJSON} {
		t.Run(string(format), func(t *testing.T) {
			var stdout bytes.Buffer
			writer := execEventWriter{
				stdout:       &stdout,
				format:       format,
				runID:        "run_usage",
				streamedText: &strings.Builder{},
			}
			writer.usage(agent.Usage{
				InputTokens:       100,
				OutputTokens:      20,
				PromptTokens:      100,
				CompletionTokens:  20,
				CachedInputTokens: 60,
				CacheWriteTokens:  10,
			})
			if writer.err != nil {
				t.Fatalf("usage: %v", writer.err)
			}
			var payload map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
				t.Fatalf("decode output %q: %v", stdout.String(), err)
			}
			cachedKey, writeKey := "cached_input_tokens", "cache_write_tokens"
			if format == execOutputStreamJSON {
				cachedKey, writeKey = "cachedInputTokens", "cacheWriteTokens"
			}
			if payload[cachedKey] != float64(60) || payload[writeKey] != float64(10) {
				t.Fatalf("cache usage missing from payload: %#v", payload)
			}
		})
	}
}

func readyPricing(models map[string]qgaccount.ModelInfo) *execPricing {
	done := make(chan struct{})
	close(done)
	return &execPricing{done: done, models: models}
}

func TestExecWriterStreamJSONUsageCarriesCost(t *testing.T) {
	pricing := readyPricing(map[string]qgaccount.ModelInfo{
		"kimi-k3": {
			ID:         "kimi-k3",
			PricingBRL: qgaccount.ModelPricing{Currency: "BRL", Input: "2", Output: "10"},
			Pricing:    qgaccount.ModelPricing{Currency: "USD", Input: "0.4", Output: "2"},
		},
	})
	var stdout bytes.Buffer
	writer := execEventWriter{
		stdout:       &stdout,
		format:       execOutputStreamJSON,
		runID:        "run_cost",
		streamedText: &strings.Builder{},
		model:        "kimi-k3",
		pricing:      pricing,
	}
	writer.usage(agent.Usage{InputTokens: 100_000, PromptTokens: 100_000, OutputTokens: 10_000, CompletionTokens: 10_000})
	if writer.err != nil {
		t.Fatalf("usage: %v", writer.err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if got := payload["costBrl"].(float64); got < 0.2999 || got > 0.3001 {
		t.Fatalf("costBrl = %v, want 0.3 (%#v)", got, payload)
	}
	if got := payload["costUsd"].(float64); got < 0.0599 || got > 0.0601 {
		t.Fatalf("costUsd = %v, want 0.06 (%#v)", got, payload)
	}
}

func TestExecWriterStreamJSONUsageOmitsCostWithoutPricing(t *testing.T) {
	for name, pricing := range map[string]*execPricing{
		"no provider pricing": nil,
		"unknown model":       readyPricing(map[string]qgaccount.ModelInfo{}),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			writer := execEventWriter{stdout: &stdout, format: execOutputStreamJSON, runID: "r", streamedText: &strings.Builder{}, model: "m", pricing: pricing}
			writer.usage(agent.Usage{InputTokens: 10, PromptTokens: 10})
			if strings.Contains(stdout.String(), "costBrl") || strings.Contains(stdout.String(), "costUsd") {
				t.Fatalf("cost fields must be omitted: %s", stdout.String())
			}
		})
	}
}
