package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// setOrqAutoCompactLimit adds preferences.autoCompactLimit to the hermetic
// user config the orchestrator harness wrote.
func setOrqAutoCompactLimit(t *testing.T, limit int) {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qg", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["preferences"] = map[string]any{"autoCompactLimit": limit}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// orqAutoCompactReplies answers tool-bearing requests with update_plan calls
// carrying large padding arguments so the conversation outgrows a 20k session
// limit while staying far below the automatic threshold of the fallback
// window; tool-less requests are the compaction summarizer and get the marker.
func orqAutoCompactReplies(summaryMarker string, toolTurns int) func(orqRequest, int) orqReply {
	return func(req orqRequest, mainIndex int) orqReply {
		switch {
		case mainIndex < 0:
			return orqReply{status: http.StatusOK, body: orqSSEText(summaryMarker)}
		case mainIndex <= toolTurns:
			args := `{"plan":[{"content":"orq autocompact step ` + strconv.Itoa(mainIndex) + ` ` + strings.Repeat("padding ", 2000) + `","status":"in_progress"}]}`
			return orqReply{status: http.StatusOK, body: orqSSEToolCall("call_"+strconv.Itoa(mainIndex), "update_plan", args)}
		default:
			return orqReply{status: http.StatusOK, body: orqSSEText("READY")}
		}
	}
}

// preferences.autoCompactLimit reaches the real `qg exec` agent loop: with a
// 20k limit a ~30k-token conversation triggers the summarizer mid-run, while
// the same conversation without the key never calls it.
func TestExecAutoCompactLimitProactive(t *testing.T) {
	const summaryMarker = "ORQ-AUTOCOMPACT-SUMMARY-9d2f"
	const toolTurns = 8

	t.Run("limit compacts", func(t *testing.T) {
		h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}))
		setOrqAutoCompactLimit(t, 20000)
		h.reply = orqAutoCompactReplies(summaryMarker, toolTurns)
		h.exec("--max-turns", "30")

		requests := h.snapshot()
		summaryIndex := -1
		for i, req := range requests {
			if req.tools == 0 {
				summaryIndex = i
				break
			}
		}
		if summaryIndex < 0 {
			t.Fatal("with autoCompactLimit set, no tool-less summarizer request was sent; the limit never reached the agent loop")
		}
		propagated := false
		for _, req := range requests[summaryIndex+1:] {
			if req.tools == 0 {
				continue
			}
			for _, message := range req.messages {
				if strings.Contains(message.content, summaryMarker) {
					propagated = true
					break
				}
			}
		}
		if !propagated {
			t.Fatal("the summarizer ran but no later main request carries its summary; the conversation was not compacted")
		}
	})

	t.Run("without the key it does not", func(t *testing.T) {
		h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}))
		h.reply = orqAutoCompactReplies(summaryMarker, toolTurns)
		h.exec("--max-turns", "30")

		for i, req := range h.snapshot() {
			if req.tools == 0 {
				t.Fatalf("request %d has no tools; without a limit the same conversation must never call the summarizer", i+1)
			}
		}
	})
}
