package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

type capturedWireMessage struct {
	Role       string            `json:"role"`
	Content    json.RawMessage   `json:"content"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []json.RawMessage `json:"tool_calls,omitempty"`
}

type capturedWireRequest struct {
	Model          string                `json:"model"`
	Messages       []capturedWireMessage `json:"messages"`
	Tools          []json.RawMessage     `json:"tools"`
	PromptCacheKey string                `json:"prompt_cache_key,omitempty"`
}

func decodeCapturedWire(t *testing.T, raw string) capturedWireRequest {
	t.Helper()
	var req capturedWireRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode wire request: %v\nraw=%s", err, raw)
	}
	return req
}

func canonicalWireJSON(t *testing.T, v any) string {
	t.Helper()
	var normalized any
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal wire value: %v", err)
	}
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatalf("unmarshal wire value: %v", err)
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("re-marshal canonical wire value: %v", err)
	}
	return string(out)
}

func runOrqExec(t *testing.T, h *orqHarness, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"exec", "--cwd", h.workdir}, args...)
	var stdout, stderr bytes.Buffer
	if code := Run(fullArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("qg %s exit=%d\nstdout=%s\nstderr=%s", strings.Join(fullArgs, " "), code, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func TestPreflightRejectsSideSessions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := sessions.NewStore(sessions.StoreOptions{})
	if _, err := store.Create(sessions.CreateInput{SessionID: "side", SessionKind: sessions.SessionKindSide}); err != nil {
		t.Fatalf("create side session: %v", err)
	}

	err := preflightExecSession(execOptions{resumeLatest: true})
	if err == nil || !strings.Contains(err.Error(), "No QG sessions available to resume") {
		t.Fatalf("preflightExecSession error = %v, want no resumable sessions", err)
	}

	err = preflightExecSession(execOptions{resume: "side"})
	if err == nil || !strings.Contains(err.Error(), "QG session is not resumable: side") {
		t.Fatalf("explicit side-session preflight error = %v, want non-resumable rejection", err)
	}
}

func TestExecResumeSmallSessionReplaysHistoryAndPreservesCachePrefix(t *testing.T) {
	h := newOrqHarness(t, nil)
	noteFile := filepath.Join(h.workdir, "notas.txt")
	if err := os.WriteFile(noteFile, []byte("conteúdo exato da nota 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	callStep := 0
	h.reply = func(_ orqRequest, _ int) orqReply {
		callStep++
		switch callStep {
		case 1:
			return orqReply{status: http.StatusOK, body: orqSSEToolCall("call_read_1", "read_file", `{"path":"notas.txt"}`)}
		case 2:
			return orqReply{status: http.StatusOK, body: orqSSEText("primeira resposta concluída.")}
		case 3:
			return orqReply{status: http.StatusOK, body: orqSSEText("segunda resposta concluída.")}
		default:
			return orqReply{status: http.StatusOK, body: orqSSEText("terceira resposta concluída.")}
		}
	}

	runOrqExec(t, h, "--init-session-id", "sess_small_1", "leia notas.txt e resuma")
	origRequests := h.snapshot()
	if len(origRequests) != 2 {
		t.Fatalf("original session requests = %d, want 2", len(origRequests))
	}
	origLast := decodeCapturedWire(t, origRequests[1].raw)

	// Drift the workspace between runs by adding a new file so a fresh system
	// prompt would differ from the original session's frozen system prompt.
	if err := os.WriteFile(filepath.Join(h.workdir, "extra.txt"), []byte("novo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Explicit --resume <id>: 1st call must carry full structured history and
	// preserve the exact prefix (tools + messages) of the original last call.
	runOrqExec(t, h, "--resume", "sess_small_1", "qual era o número na nota?")
	afterExplicit := h.snapshot()
	if len(afterExplicit) != 3 {
		t.Fatalf("total requests after explicit resume = %d, want 3", len(afterExplicit))
	}
	resumedRaw := afterExplicit[2].raw
	for _, banned := range []string{"Continuing QG session", "Previous session context:"} {
		if strings.Contains(resumedRaw, banned) {
			t.Fatalf("resumed 1st call must send real messages instead of textual summary (%q)", banned)
		}
	}
	resumedFirst := decodeCapturedWire(t, resumedRaw)
	if canonicalWireJSON(t, resumedFirst.Tools) != canonicalWireJSON(t, origLast.Tools) {
		t.Fatal("resumed 1st call tools differ from original session last call")
	}
	if len(resumedFirst.Messages) != len(origLast.Messages)+2 {
		t.Fatalf("resumed messages = %d, want %d (orig last %d + assistant answer + new user prompt)",
			len(resumedFirst.Messages), len(origLast.Messages)+2, len(origLast.Messages))
	}
	gotPrefix := canonicalWireJSON(t, resumedFirst.Messages[:len(origLast.Messages)])
	wantPrefix := canonicalWireJSON(t, origLast.Messages)
	if gotPrefix != wantPrefix {
		t.Fatalf("resumed 1st call prefix differs from original session last call:\n%s", orqDiff(wantPrefix, gotPrefix))
	}
	if assistantTurn := resumedFirst.Messages[len(origLast.Messages)]; assistantTurn.Role != "assistant" || orqContentText(assistantTurn.Content) != "primeira resposta concluída." {
		t.Fatalf("resumed message after prefix = %+v, want original final assistant answer", assistantTurn)
	}
	lastUser := resumedFirst.Messages[len(resumedFirst.Messages)-1]
	if lastUser.Role != "user" || !strings.HasSuffix(orqContentText(lastUser.Content), "qual era o número na nota?") {
		t.Fatalf("resumed trailing user message = (%q, %q)", lastUser.Role, orqContentText(lastUser.Content))
	}

	// Bare --resume (resumeLatest): must also replay the accumulated history
	// with prefix matching the previous run's call.
	runOrqExec(t, h, "--resume", "--prompt", "e agora?")
	afterLatest := h.snapshot()
	if len(afterLatest) != 4 {
		t.Fatalf("total requests after bare --resume = %d, want 4", len(afterLatest))
	}
	latestFirst := decodeCapturedWire(t, afterLatest[3].raw)
	if len(latestFirst.Messages) != len(resumedFirst.Messages)+2 {
		t.Fatalf("bare --resume messages = %d, want %d", len(latestFirst.Messages), len(resumedFirst.Messages)+2)
	}
	if canonicalWireJSON(t, latestFirst.Messages[:len(resumedFirst.Messages)]) != canonicalWireJSON(t, resumedFirst.Messages) {
		t.Fatal("bare --resume 1st call prefix differs from previous resumed call")
	}

	// Older session without continuation.json: drop the snapshot and verify
	// event-log reconstruction still replays structured messages + tool call/result.
	store := sessions.NewStore(sessions.StoreOptions{})
	if err := store.InvalidateContinuation("sess_small_1"); err != nil {
		t.Fatal(err)
	}
	runOrqExec(t, h, "--resume", "sess_small_1", "confirme sem snapshot")
	afterRecon := h.snapshot()
	if len(afterRecon) != 5 {
		t.Fatalf("total requests after snapshot-less resume = %d, want 5", len(afterRecon))
	}
	reconWire := decodeCapturedWire(t, afterRecon[4].raw)
	if strings.Contains(afterRecon[4].raw, "Previous session context:") {
		t.Fatal("snapshot-less resume must reconstruct structured messages instead of textual summary")
	}
	if len(reconWire.Messages) != 10 || reconWire.Messages[2].Role != "assistant" || len(reconWire.Messages[2].ToolCalls) != 1 || reconWire.Messages[3].Role != "tool" || reconWire.Messages[3].ToolCallID != "call_read_1" {
		t.Fatalf("snapshot-less reconstructed messages lost tool call/result order: len=%d", len(reconWire.Messages))
	}
}

func TestExecResumeOversizedSessionFallsBackToSummary(t *testing.T) {
	t.Run("exceeds model context window", func(t *testing.T) {
		h := newOrqHarness(t, nil)
		setOrqAutoCompactLimit(t, config.AutoCompactOff)

		store := sessions.NewStore(sessions.StoreOptions{})
		sess, err := store.Create(sessions.CreateInput{
			SessionID: "sess_oversized_window",
			Cwd:       h.workdir,
			ModelID:   "orq-model",
			Provider:  "orq",
		})
		if err != nil {
			t.Fatal(err)
		}
		hugeOutput := "HUGE-WINDOW-PAYLOAD-" + strings.Repeat("x", 850_000)
		for _, ev := range []sessions.AppendEventInput{
			{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "leia o dump enorme"}},
			{Type: sessions.EventToolCall, Payload: map[string]any{"id": "c_big", "name": "read_file", "arguments": `{"path":"dump.log"}`}},
			{Type: sessions.EventToolResult, Payload: map[string]any{"toolCallId": "c_big", "name": "read_file", "status": "ok", "output": hugeOutput}},
			{Type: sessions.EventMessage, Payload: map[string]any{"role": "assistant", "content": "analisei o dump enorme."}},
		} {
			if _, err := store.AppendEvent(sess.SessionID, ev); err != nil {
				t.Fatal(err)
			}
		}

		runOrqExec(t, h, "--resume", sess.SessionID, "continue a análise")
		reqs := h.snapshot()
		if len(reqs) != 1 {
			t.Fatalf("oversized resume requests = %d, want 1 (offline summary without paid summarizer call)", len(reqs))
		}
		if len(reqs[0].messages) != 2 {
			t.Fatalf("oversized fallback messages = %d, want 2 [system, summary+prompt]", len(reqs[0].messages))
		}
		userBody := reqs[0].messages[1].content
		for _, want := range []string{"Continuing QG session sess_oversized_window.", "Previous session context:", "analisei o dump enorme.", "continue a análise"} {
			if !strings.Contains(userBody, want) {
				t.Fatalf("fallback summary missing %q:\n%s", want, userBody)
			}
		}
		if strings.Contains(reqs[0].raw, hugeOutput) {
			t.Fatal("oversized raw tool output must not be sent when history exceeds context window")
		}
	})

	t.Run("respects configured autoCompactLimit", func(t *testing.T) {
		seedMediumSession := func(t *testing.T, h *orqHarness, id string) string {
			t.Helper()
			store := sessions.NewStore(sessions.StoreOptions{})
			sess, err := store.Create(sessions.CreateInput{
				SessionID: id,
				Cwd:       h.workdir,
				ModelID:   "orq-model",
				Provider:  "orq",
			})
			if err != nil {
				t.Fatal(err)
			}
			mediumOutput := "MEDIUM-TOOL-OUTPUT-" + strings.Repeat("m", 110_000)
			for _, ev := range []sessions.AppendEventInput{
				{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "inspecione o arquivo médio"}},
				{Type: sessions.EventToolCall, Payload: map[string]any{"id": "c_med", "name": "read_file", "arguments": `{"path":"medio.txt"}`}},
				{Type: sessions.EventToolResult, Payload: map[string]any{"toolCallId": "c_med", "name": "read_file", "status": "ok", "output": mediumOutput}},
				{Type: sessions.EventMessage, Payload: map[string]any{"role": "assistant", "content": "inspeção concluída."}},
			} {
				if _, err := store.AppendEvent(sess.SessionID, ev); err != nil {
					t.Fatal(err)
				}
			}
			return mediumOutput
		}

		// With autoCompactLimit = 20000 tokens, ~28k tokens of history exceeds
		// the limit and falls back to the textual session summary.
		limited := newOrqHarness(t, nil)
		setOrqAutoCompactLimit(t, 20_000)
		mediumOutput := seedMediumSession(t, limited, "sess_autolimit_exceeded")
		runOrqExec(t, limited, "--resume", "sess_autolimit_exceeded", "próximo passo")
		limitedReqs := limited.snapshot()
		if len(limitedReqs) != 1 {
			t.Fatalf("limited resume requests = %d, want 1 (summary fallback, not paid summarizer)", len(limitedReqs))
		}
		if !strings.Contains(limitedReqs[0].messages[1].content, "Previous session context:") {
			t.Fatalf("expected summary fallback when exceeding autoCompactLimit, got:\n%s", limitedReqs[0].messages[1].content)
		}
		if strings.Contains(limitedReqs[0].raw, mediumOutput) {
			t.Fatal("history exceeding autoCompactLimit must fall back to summary instead of sending raw output")
		}

		// Without autoCompactLimit, the exact same ~28k-token session fits the
		// 200k model window and replays the full history verbatim.
		unlimited := newOrqHarness(t, nil)
		mediumOutputFit := seedMediumSession(t, unlimited, "sess_autolimit_fits")
		runOrqExec(t, unlimited, "--resume", "sess_autolimit_fits", "próximo passo")
		unlimitedReqs := unlimited.snapshot()
		if len(unlimitedReqs) != 1 {
			t.Fatalf("unlimited resume requests = %d, want 1", len(unlimitedReqs))
		}
		if strings.Contains(unlimitedReqs[0].raw, "Previous session context:") {
			t.Fatal("session fitting default window must replay real history, not summary")
		}
		if !strings.Contains(unlimitedReqs[0].raw, mediumOutputFit) {
			t.Fatal("session fitting default window must carry the full prior tool output")
		}
	})
}

func TestExecResumeCompactedSessionStartsFromRecordedSummary(t *testing.T) {
	h := newOrqHarness(t, nil)
	store := sessions.NewStore(sessions.StoreOptions{})
	sess, err := store.Create(sessions.CreateInput{
		SessionID: "sess_compacted_1",
		Cwd:       h.workdir,
		ModelID:   "orq-model",
		Provider:  "orq",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ev := range []sessions.AppendEventInput{
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "pergunta antiga descartada"}},
		{Type: sessions.EventToolCall, Payload: map[string]any{"id": "c_old", "name": "read_file", "arguments": `{"path":"antigo.txt"}`}},
		{Type: sessions.EventToolResult, Payload: map[string]any{"toolCallId": "c_old", "name": "read_file", "status": "ok", "output": "DETALHE-ANTIGO-COMPACTADO"}},
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "assistant", "content": "resposta antiga descartada"}},
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "pergunta preservada após compactação"}},
		{Type: sessions.EventToolCall, Payload: map[string]any{"id": "c_recent", "name": "read_file", "arguments": `{"path":"recente.txt"}`}},
		{Type: sessions.EventToolResult, Payload: map[string]any{"toolCallId": "c_recent", "name": "read_file", "status": "ok", "output": "SAIDA-FERRAMENTA-POS-COMPACTACAO"}},
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "assistant", "content": "resposta preservada após compactação"}},
	} {
		if _, err := store.AppendEvent(sess.SessionID, ev); err != nil {
			t.Fatal(err)
		}
	}

	// Write a pre-compaction snapshot so resume must also detect that the
	// later EventCompaction invalidated it and start from the recorded summary.
	metaBefore, err := store.Get(sess.SessionID)
	if err != nil || metaBefore == nil {
		t.Fatalf("Get before compaction: %v", err)
	}
	if err := store.WriteContinuation(sessions.ContinuationSnapshot{
		SessionID: sess.SessionID,
		ModelID:   "orq-model",
		Provider:  "orq",
		EventSeq:  metaBefore.EventCount,
		Messages: []qgruntime.Message{
			{Role: qgruntime.MessageRoleSystem, Content: "stale system"},
			{Role: qgruntime.MessageRoleUser, Content: "pergunta antiga descartada"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PlanCompaction(sess.SessionID, sessions.CompactionOptions{
		PreserveLast: 4,
		SkipPrompt:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	const recordedSummary = "RESUMO-GRAVADO-COMPACTACAO: contexto antigo consolidado."
	if _, err := store.RecordCompaction(sess.SessionID, sessions.RecordCompactionInput{
		Plan:    plan,
		Summary: recordedSummary,
	}); err != nil {
		t.Fatal(err)
	}

	runOrqExec(t, h, "--resume", sess.SessionID, "nova pergunta depois de compactar")
	reqs := h.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("compacted resume requests = %d, want 1", len(reqs))
	}
	wire := decodeCapturedWire(t, reqs[0].raw)
	for _, banned := range []string{
		"Continuing QG session",
		"Previous session context:",
		"pergunta antiga descartada",
		"DETALHE-ANTIGO-COMPACTADO",
		"resposta antiga descartada",
	} {
		if strings.Contains(reqs[0].raw, banned) {
			t.Fatalf("compacted resume wire must not contain %q", banned)
		}
	}

	// Expected structured messages:
	// [0] system
	// [1] user ("[Summary of earlier conversation]\n" + recordedSummary)
	// [2] user ("pergunta preservada após compactação")
	// [3] assistant (tool_calls: c_recent)
	// [4] tool (c_recent: "SAIDA-FERRAMENTA-POS-COMPACTACAO")
	// [5] assistant ("resposta preservada após compactação")
	// [6] user ("nova pergunta depois de compactar")
	if len(wire.Messages) != 7 {
		t.Fatalf("compacted resume wire messages = %d, want 7", len(wire.Messages))
	}
	if wire.Messages[0].Role != "system" {
		t.Fatalf("message[0] role = %q, want system", wire.Messages[0].Role)
	}
	wantSummaryMsg := "[Summary of earlier conversation]\n" + recordedSummary
	if wire.Messages[1].Role != "user" || orqContentText(wire.Messages[1].Content) != wantSummaryMsg {
		t.Fatalf("message[1] = (%q, %q), want (user, %q)", wire.Messages[1].Role, orqContentText(wire.Messages[1].Content), wantSummaryMsg)
	}
	if wire.Messages[2].Role != "user" || orqContentText(wire.Messages[2].Content) != "pergunta preservada após compactação" {
		t.Fatalf("message[2] = (%q, %q)", wire.Messages[2].Role, orqContentText(wire.Messages[2].Content))
	}
	if wire.Messages[3].Role != "assistant" || len(wire.Messages[3].ToolCalls) != 1 {
		t.Fatalf("message[3] = %+v, want assistant with 1 tool call", wire.Messages[3])
	}
	if wire.Messages[4].Role != "tool" || wire.Messages[4].ToolCallID != "c_recent" || orqContentText(wire.Messages[4].Content) != "SAIDA-FERRAMENTA-POS-COMPACTACAO" {
		t.Fatalf("message[4] = %+v, want preserved tool result", wire.Messages[4])
	}
	if wire.Messages[5].Role != "assistant" || orqContentText(wire.Messages[5].Content) != "resposta preservada após compactação" {
		t.Fatalf("message[5] = (%q, %q)", wire.Messages[5].Role, orqContentText(wire.Messages[5].Content))
	}
	if wire.Messages[6].Role != "user" || orqContentText(wire.Messages[6].Content) != "nova pergunta depois de compactar" {
		t.Fatalf("message[6] = (%q, %q)", wire.Messages[6].Role, orqContentText(wire.Messages[6].Content))
	}
}
