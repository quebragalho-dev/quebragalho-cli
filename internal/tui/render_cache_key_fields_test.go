package tui

import (
	"context"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// baseRenderKeyInputs builds one stable, cacheable set of inputs for the row
// cache key table. The row is a tool result so every text field the key carries
// reads on screen through its card.
func baseRenderKeyInputs() (model, transcriptRow, rowContext, cardRenderOptions, int, bool) {
	m := newModel(context.Background(), Options{Cwd: "/work/qg"})
	m.cwd = "/work/qg"
	row := transcriptRow{
		kind:   rowToolResult,
		id:     "call_1",
		text:   "tool result: edit_file ok",
		tool:   "edit_file",
		status: tools.StatusOK,
		detail: "+added one\n-removed one",
		arg:    "internal/tui/sidebar.go",
		runID:  3,
		meta:   map[string]string{"amount": "19/20", "currency": "BRL"},
		attachments: transcriptAttachmentSummary{
			images:    1,
			documents: 2,
		},
	}
	rc := rowContext{
		hints: map[string]string{rcKey(3, "call_1"): "internal/tui/sidebar.go"},
		args:  map[string]string{rcKey(3, "call_1"): "pattern"},
		auto:  map[string]bool{rcKey(3, "call_1"): true},
	}
	opts := cardRenderOptions{bodyCap: cardBodyMaxLines, cwd: "/work/qg"}
	return m, row, rc, opts, 80, false
}

// renderTwice primes the cache with base inputs and renders once more with
// mutated inputs. It reports how many times the render func ran: 1 means the
// mutated inputs hit the base entry (a stale drawing), 2 means they missed.
func renderTwice(t *testing.T, mutate func(*model, *transcriptRow, *rowContext, *cardRenderOptions, *int, *bool)) int {
	t.Helper()
	m, row, rc, opts, width, flush := baseRenderKeyInputs()
	cache := replaceDefaultRenderCache(t, newStaticRenderCache(64, 1<<20))
	mutate(&m, &row, &rc, &opts, &width, &flush)
	renders := 0
	render := func() string {
		renders++
		return "drawn"
	}
	baseM, baseRow, baseRC, baseOpts, baseWidth, baseFlush := baseRenderKeyInputs()
	baseKey, baseStable := baseM.renderRowCacheKey(baseRow, baseWidth, baseRC, baseOpts, baseFlush)
	if !baseStable {
		t.Fatalf("base inputs must be stable")
	}
	_ = cache.render(baseKey, baseStable, render)
	mutKey, mutStable := m.renderRowCacheKey(row, width, rc, opts, flush)
	if !mutStable {
		t.Fatalf("mutated inputs must stay stable for the field table")
	}
	_ = cache.render(mutKey, mutStable, render)
	return renders
}

func TestRenderRowCacheKeyChangesMiss(t *testing.T) {
	permBase := func() *agent.PermissionEvent {
		return &agent.PermissionEvent{ToolCallID: "call_1", ToolName: "bash", Action: agent.PermissionActionAllow, PermissionMode: agent.PermissionModeAuto}
	}
	askBase := func() *agent.AskUserRequest {
		return &agent.AskUserRequest{ToolCallID: "ask_1", Header: "h", Questions: []agent.AskUserQuestion{{Question: "q1", Options: []string{"a", "b"}}}}
	}
	cases := []struct {
		name   string
		mutate func(*model, *transcriptRow, *rowContext, *cardRenderOptions, *int, *bool)
	}{
		{"width", func(_ *model, _ *transcriptRow, _ *rowContext, _ *cardRenderOptions, width *int, _ *bool) {
			*width = 81
		}},
		{"flush", func(_ *model, _ *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, flush *bool) {
			*flush = true
		}},
		{"bodyCap", func(_ *model, _ *transcriptRow, _ *rowContext, opts *cardRenderOptions, _ *int, _ *bool) {
			opts.bodyCap = 1
		}},
		{"optsExpanded", func(_ *model, _ *transcriptRow, _ *rowContext, opts *cardRenderOptions, _ *int, _ *bool) {
			opts.expanded = true
		}},
		{"cwd", func(_ *model, _ *transcriptRow, _ *rowContext, opts *cardRenderOptions, _ *int, _ *bool) {
			opts.cwd = "/other"
		}},
		{"kind", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.kind = rowAssistant
		}},
		{"id", func(_ *model, row *transcriptRow, rc *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.id = "call_2"
			// Keep the rc-derived hint/arg/auto identical so only the id field changes.
			rc.hints[rcKey(3, "call_2")] = rc.hints[rcKey(3, "call_1")]
			rc.args[rcKey(3, "call_2")] = rc.args[rcKey(3, "call_1")]
			rc.auto[rcKey(3, "call_2")] = rc.auto[rcKey(3, "call_1")]
		}},
		{"text", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.text = "tool result: edit_file ok (2)"
		}},
		{"metaAmount", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.meta["amount"] = "1/2"
		}},
		{"metaCurrency", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.meta["currency"] = "USD"
		}},
		{"tool", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.tool = "write_file"
		}},
		{"status", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.status = tools.StatusError
		}},
		{"detail", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.detail = "+other"
		}},
		{"arg", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.arg = "other.go"
		}},
		{"runID", func(_ *model, row *transcriptRow, rc *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.runID = 4
			// Keep the rc-derived hint/arg/auto identical so only the runID field changes.
			rc.hints[rcKey(4, "call_1")] = rc.hints[rcKey(3, "call_1")]
			rc.args[rcKey(4, "call_1")] = rc.args[rcKey(3, "call_1")]
			rc.auto[rcKey(4, "call_1")] = rc.auto[rcKey(3, "call_1")]
		}},
		{"expanded", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.expanded = true
		}},
		{"final", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.final = true
		}},
		{"turnTools", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.turnTools = 2
		}},
		{"turnElapsed", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.turnElapsed = 2 * time.Second
		}},
		{"attachImages", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.attachments.images = 2
		}},
		{"attachDocs", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.attachments.documents = 3
		}},
		{"fileSelected", func(m *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.changedFiles = []string{"internal/tui/sidebar.go"}
			m.selectedFile = "internal/tui/sidebar.go"
		}},
		{"rcHint", func(_ *model, _ *transcriptRow, rc *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			rc.hints[rcKey(3, "call_1")] = "other hint"
		}},
		{"rcArg", func(_ *model, _ *transcriptRow, rc *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			rc.args[rcKey(3, "call_1")] = "other arg"
		}},
		{"rcAuto", func(_ *model, _ *transcriptRow, rc *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			rc.auto[rcKey(3, "call_1")] = false
		}},
		{"permission", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.kind = rowPermission
			row.id = "call_1"
			perm := permBase()
			perm.ToolName = "other-tool"
			row.permission = perm
		}},
		{"askUser", func(_ *model, row *transcriptRow, _ *rowContext, _ *cardRenderOptions, _ *int, _ *bool) {
			row.kind = rowAskUser
			row.id = "ask_1"
			ask := askBase()
			ask.Header = "other header"
			row.askUser = ask
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The permission/askUser cases replace the base tool-result row, so
			// prime the cache with the same replaced row to isolate the single
			// field change under test.
			if tc.name == "permission" || tc.name == "askUser" {
				m, row, rc, opts, width, flush := baseRenderKeyInputs()
				tc.mutate(&m, &row, &rc, &opts, &width, &flush)
				cache := replaceDefaultRenderCache(t, newStaticRenderCache(64, 1<<20))
				renders := 0
				render := func() string {
					renders++
					return "drawn"
				}
				firstKey, firstStable := m.renderRowCacheKey(row, width, rc, opts, flush)
				if !firstStable {
					t.Fatalf("base inputs must be stable")
				}
				_ = cache.render(firstKey, firstStable, render)
				// Flip one more permission/askUser detail on top of the replaced row.
				if tc.name == "permission" {
					row.permission.Reason = "needs approval"
				} else {
					row.askUser.Questions[0].Question = "other question"
				}
				secondKey, secondStable := m.renderRowCacheKey(row, width, rc, opts, flush)
				if !secondStable {
					t.Fatalf("mutated inputs must stay stable")
				}
				_ = cache.render(secondKey, secondStable, render)
				if renders != 2 {
					t.Fatalf("changing %s ran the render func %d times, want 2 (a miss)", tc.name, renders)
				}
				return
			}
			if got := renderTwice(t, tc.mutate); got != 2 {
				t.Fatalf("changing %s ran the render func %d times, want 2 (a miss)", tc.name, got)
			}
		})
	}
}

// TestRenderRowCacheKeyVersionPinsEntries guards the version field: entries
// recorded under the current version never collide with another version.
func TestRenderRowCacheKeyVersionPinsEntries(t *testing.T) {
	m, row, rc, opts, width, flush := baseRenderKeyInputs()
	key, stable := m.renderRowCacheKey(row, width, rc, opts, flush)
	if !stable {
		t.Fatalf("base inputs must be stable")
	}
	if key.version != renderRowCacheVersion {
		t.Fatalf("key version = %q, want %q", key.version, renderRowCacheVersion)
	}
	if key.version != "render-row-v1" {
		t.Fatalf("key version = %q, want the documented render-row-v1", key.version)
	}
}

// BenchmarkStreamRowDrawHits measures the per-frame cost of redrawing one
// settled stream row: the cache is primed, so every op is a hit and the work
// is key building plus the map lookup. The old string key copied the whole row
// text on every frame; the struct key only copies headers.
func BenchmarkStreamRowDrawHits(b *testing.B) {
	m, row, rc, opts, width, flush := baseRenderKeyInputs()
	row.detail = baseRenderKeyInputsDetail()
	rc = rowContext{}
	cache := newStaticRenderCache(64, 1<<20)
	previous := defaultRenderCache
	defaultRenderCache = cache
	defer func() { defaultRenderCache = previous }()
	_ = m.renderRow(row, width, rc)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderRow(row, width, rc)
	}
	_ = opts
	_ = flush
}

func baseRenderKeyInputsDetail() string {
	detail := "+added one\n-removed one\n"
	for i := 0; i < 200; i++ {
		detail += "+line of streamed tool output that stays on screen for the whole turn\n"
	}
	return detail
}
