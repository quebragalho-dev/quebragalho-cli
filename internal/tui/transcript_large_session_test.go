package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// largeSessionEvents builds a deterministic session shaped like a long real
// coding session: every turn has a user prompt, four tool calls (a command, a
// file read, a grep and an edit with a diff), the per-call usage receipt and a
// markdown answer. The tool output is what makes rows expensive to render
// (syntax-highlighted code, diff hunks), so it is sized like real output.
func largeSessionEvents(turns int) []sessions.Event {
	events := make([]sessions.Event, 0, turns*14)
	add := func(eventType sessions.EventType, payload map[string]any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		events = append(events, sessions.Event{
			ID:        fmt.Sprintf("evt_%06d", len(events)),
			SessionID: "qg_large_session",
			Sequence:  len(events) + 1,
			Type:      eventType,
			CreatedAt: "2026-11-03T12:00:00Z",
			Payload:   raw,
		})
	}
	for turn := 0; turn < turns; turn++ {
		add(sessions.EventMessage, map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("Turno %d: ajuste o handler %d para validar o corpo antes de gravar e rode os testes do pacote.", turn, turn),
		})
		path := fmt.Sprintf("/work/qg/internal/server/handler_%d.go", turn)
		calls := []struct {
			name      string
			arguments map[string]any
			output    string
			preview   string
		}{
			{"exec_command", map[string]any{"cmd": fmt.Sprintf("go test ./internal/server/... -run Handler%d", turn)}, largeSessionCommandOutput(turn), ""},
			{"read_file", map[string]any{"path": path}, largeSessionFileRead(path, turn), ""},
			{"grep", map[string]any{"pattern": fmt.Sprintf("handler%d", turn), "output_mode": "content"}, largeSessionGrepOutput(turn), ""},
			{"edit_file", map[string]any{"path": path, "old_string": "return nil", "new_string": "return validate(r)"}, "Successfully edited " + path + " (replaced 1 occurrence).", largeSessionDiff(path, turn)},
		}
		for index, call := range calls {
			id := fmt.Sprintf("call_%d_%d", turn, index)
			arguments, _ := json.Marshal(call.arguments)
			add(sessions.EventToolCall, map[string]any{"id": id, "name": call.name, "arguments": string(arguments)})
			result := map[string]any{"toolCallId": id, "name": call.name, "output": call.output, "status": "ok"}
			if call.preview != "" {
				result["displayPreview"] = call.preview
				result["changedFiles"] = []string{path}
			}
			add(sessions.EventToolResult, result)
			add(sessions.EventProviderUsage, map[string]any{
				"call_label":    fmt.Sprintf("kimi-k3.1-pro · %d tok · R$ 0,03", 50000+turn*10+index),
				"model":         "kimi-k3.1-pro",
				"cost_exact":    "0.03",
				"cost_currency": "BRL",
				"call_tokens":   fmt.Sprintf("%d", 50000+turn*10+index),
			})
		}
		add(sessions.EventMessage, map[string]any{
			"role":    "assistant",
			"content": largeSessionAnswer(turn),
		})
	}
	return events
}

func largeSessionGoLines(turn int, count int) []string {
	lines := make([]string, 0, count)
	for i := 0; len(lines) < count; i++ {
		lines = append(lines,
			fmt.Sprintf("func handler%d_%d(w http.ResponseWriter, r *http.Request) error {", turn, i),
			"\tif err := validate(r); err != nil {",
			fmt.Sprintf("\t\treturn fmt.Errorf(\"handler %d: %%w\", err)", i),
			"\t}",
			fmt.Sprintf("\tvalue := strings.TrimSpace(r.FormValue(\"field_%d\"))", i),
			"\treturn store.Save(r.Context(), value)",
			"}",
		)
	}
	return lines[:count]
}

func largeSessionFileRead(path string, turn int) string {
	lines := largeSessionGoLines(turn, 60)
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s (%d lines)\n\n", path, len(lines))
	for index, line := range lines {
		fmt.Fprintf(&b, "%d→%s\n", index+1, line)
	}
	return b.String()
}

func largeSessionCommandOutput(turn int) string {
	var b strings.Builder
	b.WriteString("output:\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&b, "ok  \tgithub.com/acme/app/internal/server/part%d_%d\t0.%03ds\n", turn, i, (turn*7+i*13)%1000)
	}
	b.WriteString("PASS\n")
	return b.String()
}

func largeSessionGrepOutput(turn int) string {
	var b strings.Builder
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&b, "internal/server/handler_%d.go:%d:\treturn handler%d_%d(w, r)\n", turn, 10+i*7, turn, i)
	}
	return b.String()
}

func largeSessionDiff(path string, turn int) string {
	before := largeSessionGoLines(turn, 14)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	for hunk := 0; hunk < 2; hunk++ {
		fmt.Fprintf(&b, "@@ -%d,7 +%d,7 @@\n", 10+hunk*20, 10+hunk*20)
		for index, line := range before[hunk*7 : hunk*7+7] {
			if index == 5 {
				fmt.Fprintf(&b, "-%s\n+\treturn store.SaveValidated(r.Context(), value, %d)\n", line, turn)
				continue
			}
			fmt.Fprintf(&b, " %s\n", line)
		}
	}
	return b.String()
}

func largeSessionAnswer(turn int) string {
	return fmt.Sprintf("## Handler %d\n\nAjustei a validação antes de gravar:\n\n- `validate(r)` roda antes do `store.Save`;\n- o erro volta com o contexto do handler;\n- os testes do pacote passam.\n\n```go\nif err := validate(r); err != nil {\n\treturn fmt.Errorf(\"handler %d: %%w\", err)\n}\n```\n", turn, turn)
}

// largeSessionModel resumes the synthetic session into an alt-screen model at
// a typical terminal size, the same way /resume rebuilds the transcript.
func largeSessionModel(tb testing.TB, turns int) model {
	tb.Helper()
	m := newModel(context.Background(), Options{Cwd: "/work/qg"})
	m.altScreen = true
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)
	rows := initialTranscript()
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(largeSessionEvents(turns)))
	m.transcript = rows
	m.resetFlushFrontier("")
	m, _ = m.settleTranscript()
	if m.flushed != len(m.transcript) {
		tb.Fatalf("resumed session did not settle: flushed=%d rows=%d", m.flushed, len(m.transcript))
	}
	return m
}

func updateLargeSessionModel(tb testing.TB, m model, msg tea.Msg) model {
	tb.Helper()
	next, _ := m.Update(msg)
	updated, ok := next.(model)
	if !ok {
		tb.Fatalf("Update returned %T, want model", next)
	}
	_ = updated.View()
	return updated
}

// useFreshRenderCache installs an empty row render cache with the production
// limits for the duration of the test, so its counters measure only the test.
func useFreshRenderCache(tb testing.TB) *staticRenderCache {
	tb.Helper()
	cache := newStaticRenderCache(defaultRenderCacheMaxEntries, defaultRenderCacheMaxCharacters)
	previous := defaultRenderCache
	defaultRenderCache = cache
	tb.Cleanup(func() { defaultRenderCache = previous })
	return cache
}

// largeSessionEventCosts replays the events a user produces while reading a
// resumed session — redraw, hover, wheel scroll, typing while scrolled up —
// and reports how many cached row renders each one needed after the first
// frame was drawn.
func largeSessionEventCosts(t *testing.T, turns int, prepare func(model) model) map[string]renderCacheStats {
	t.Helper()
	cache := useFreshRenderCache(t)
	m := largeSessionModel(t, turns)
	if prepare != nil {
		m = prepare(m)
	}
	_ = m.View()

	costs := map[string]renderCacheStats{}
	measure := func(name string, event func()) {
		before := cache.stats()
		event()
		after := cache.stats()
		costs[name] = renderCacheStats{
			Hits:   after.Hits - before.Hits,
			Misses: after.Misses - before.Misses,
		}
	}
	measure("redraw", func() { _ = m.View() })
	measure("hover", func() { m = updateLargeSessionModel(t, m, tea.MouseMotionMsg{X: 30, Y: 12}) })
	measure("wheel-up", func() {
		m = updateLargeSessionModel(t, m, tea.MouseWheelMsg{X: 30, Y: 12, Button: tea.MouseWheelUp})
	})
	if m.chatScrollOffset <= 0 {
		t.Fatalf("wheel up did not scroll the transcript: offset=%d", m.chatScrollOffset)
	}
	measure("redraw-scrolled", func() { _ = m.View() })
	measure("hover-scrolled", func() { m = updateLargeSessionModel(t, m, tea.MouseMotionMsg{X: 31, Y: 13}) })
	measure("key-scrolled", func() { m = updateLargeSessionModel(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"}) })
	return costs
}

// TestLargeSessionEventsCostOnlyTheViewport pins the per-event cost of the
// alt-screen transcript to what is on screen. Redrawing, hovering, scrolling
// and typing must not re-render rows that are off screen, so the work per event
// must not grow with the session: before the settled layout was memoized, the
// height and render caches (bounded LRUs walked in transcript order) evicted
// every row before reusing it, and each of these events re-rendered the whole
// history — about 100ms per event on a 300-tool-call session, which turned
// mouse movement into a minutes-long input backlog.
func TestLargeSessionEventsCostOnlyTheViewport(t *testing.T) {
	assertLargeSessionEventCostsFlat(t, nil)
}

// TestLargeSessionDetailedViewEventsCostOnlyTheViewport is the same contract for
// the detailed view (Ctrl+O), which used to rebuild and re-measure every row
// of the transcript on each frame.
func TestLargeSessionDetailedViewEventsCostOnlyTheViewport(t *testing.T) {
	assertLargeSessionEventCostsFlat(t, func(m model) model {
		m.transcriptDetailed = true
		return m
	})
}

func assertLargeSessionEventCostsFlat(t *testing.T, prepare func(model) model) {
	t.Helper()
	small := largeSessionEventCosts(t, 40, prepare)
	large := largeSessionEventCosts(t, 240, prepare)
	for _, name := range []string{"redraw", "hover", "wheel-up", "redraw-scrolled", "hover-scrolled", "key-scrolled"} {
		smallCost, largeCost := small[name], large[name]
		smallRenders := smallCost.Hits + smallCost.Misses
		largeRenders := largeCost.Hits + largeCost.Misses
		if largeRenders > smallRenders {
			t.Errorf("%s: 240 turns needed %d row renders (hits=%d misses=%d), 40 turns needed %d; the per-event cost must not grow with the session",
				name, largeRenders, largeCost.Hits, largeCost.Misses, smallRenders)
		}
	}
	// Nothing new enters the viewport on a plain redraw or a hover, so every row
	// they draw must come from the cache.
	for _, name := range []string{"redraw", "hover", "redraw-scrolled", "hover-scrolled", "key-scrolled"} {
		if misses := large[name].Misses; misses != 0 {
			t.Errorf("%s: %d render cache misses on a steady frame, want 0", name, misses)
		}
	}
}

// TestLargeSubchatFramesCostOnlyTheViewport covers the subagent drill-in, whose
// rows were never cached: every frame re-rendered the whole child session.
func TestLargeSubchatFramesCostOnlyTheViewport(t *testing.T) {
	costs := func(turns int) map[string]renderCacheStats {
		cache := useFreshRenderCache(t)
		m := largeSessionModel(t, 1)
		m.subchat = subchatState{
			active:            true,
			childSessionID:    "qg_large_child",
			childSessionTitle: "especialista",
			childRows:         transcriptRowsFromSessionEvents(largeSessionEvents(turns)),
		}
		_ = m.View()
		result := map[string]renderCacheStats{}
		measure := func(name string, event func()) {
			before := cache.stats()
			event()
			after := cache.stats()
			result[name] = renderCacheStats{Hits: after.Hits - before.Hits, Misses: after.Misses - before.Misses}
		}
		measure("redraw", func() { _ = m.View() })
		measure("hover", func() { m = updateLargeSessionModel(t, m, tea.MouseMotionMsg{X: 30, Y: 12}) })
		return result
	}
	small, large := costs(40), costs(240)
	for _, name := range []string{"redraw", "hover"} {
		smallRenders := small[name].Hits + small[name].Misses
		largeRenders := large[name].Hits + large[name].Misses
		if largeRenders > smallRenders || large[name].Misses != 0 {
			t.Errorf("subchat %s: 240 turns needed %d row renders (misses=%d), 40 turns needed %d; want a flat, fully cached frame",
				name, largeRenders, large[name].Misses, smallRenders)
		}
	}
}

// TestLargeSessionViewMatchesFullLayout is the differential check for the
// memoized layout: at every scroll position the frame must be byte-identical to
// one drawn from freshly built items laid out in full, the path that measures
// nothing and renders everything.
func TestLargeSessionViewMatchesFullLayout(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		m := largeSessionModel(t, 60)
		m.transcriptDetailed = detailed
		width := m.chatColumnWidth()
		total, maxOffset := m.chatScrollMetrics()
		if maxOffset <= 0 {
			t.Fatalf("detailed=%v: transcript fits the viewport (total=%d), the check needs scrolling", detailed, total)
		}
		for _, offset := range []int{0, 1, 7, 40, maxOffset / 3, maxOffset / 2, maxOffset - 1, maxOffset} {
			m.chatScrollOffset = offset
			// The footer carries the "↓ N more" cue, so it is rebuilt per offset.
			header, footer := m.pinnedTitleBar(width), m.footerView(width)
			if detailed {
				header = detailedTranscriptHeader(width) + "\n" + qgTheme.line.Render(strings.Repeat("-", width))
				footer = m.detailedTranscriptFooter(width)
			}
			fresh := m
			fresh.altScreenSettledWidth = 0 // bypass the snapshot: build every item from scratch
			freshItems := fresh.transcriptBodyItems(width, "", detailed)
			want := fresh.scrollableTranscriptLayoutView(header, layoutTranscriptBodyItems(freshItems), footer, width, "")
			got := m.transcriptView()
			if detailed {
				got = m.detailedTranscriptView()
			}
			if got != want {
				t.Fatalf("detailed=%v offset=%d: memoized frame differs from the full layout\nwant:\n%s\n\ngot:\n%s", detailed, offset, want, got)
			}
		}
	}
}

// BenchmarkLargeSessionLiveTurn reports the two costs a long session pays while
// a turn runs: a streaming frame (the settled history plus the live tail) and a
// row settling, which rebuilds the snapshot and measures it again. Rows that did
// not change are served by the height cache, so a settle renders only new rows.
func BenchmarkLargeSessionLiveTurn(b *testing.B) {
	for _, turns := range []int{50, 200, 800} {
		base := largeSessionModel(b, turns)
		_ = base.View()
		b.Run(fmt.Sprintf("turns=%d/streaming-frame", turns), func(b *testing.B) {
			m := base
			m.pending = true
			m.activeRunID = 1
			m.streamingText = []byte("Vou ajustar o handler e rodar os testes do pacote de novo.")
			_ = m.View()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
		b.Run(fmt.Sprintf("turns=%d/row-settles", turns), func(b *testing.B) {
			m := base
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m.transcript = append(m.transcript, transcriptRow{kind: rowSystem, text: fmt.Sprintf("nota %d", i)})
				m, _ = m.settleTranscript()
				_ = m.View()
			}
		})
	}
}

// BenchmarkLargeSessionEvents reports the cost of one user event on a resumed
// session of growing size. Each op is one Update plus the View Bubble Tea draws
// after it. The cost must stay flat as the session grows.
func BenchmarkLargeSessionEvents(b *testing.B) {
	for _, turns := range []int{50, 200, 800} {
		base := largeSessionModel(b, turns)
		_ = base.View()
		b.Run(fmt.Sprintf("turns=%d/redraw", turns), func(b *testing.B) {
			m := base
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
		b.Run(fmt.Sprintf("turns=%d/hover", turns), func(b *testing.B) {
			m := base
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m = updateLargeSessionModel(b, m, tea.MouseMotionMsg{X: 30 + i%2, Y: 12})
			}
		})
		b.Run(fmt.Sprintf("turns=%d/wheel", turns), func(b *testing.B) {
			m := updateLargeSessionModel(b, base, tea.MouseWheelMsg{X: 30, Y: 12, Button: tea.MouseWheelUp})
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				button := tea.MouseWheelUp
				if i%2 == 1 {
					button = tea.MouseWheelDown
				}
				m = updateLargeSessionModel(b, m, tea.MouseWheelMsg{X: 30, Y: 12, Button: button})
			}
		})
		b.Run(fmt.Sprintf("turns=%d/key-scrolled", turns), func(b *testing.B) {
			m := updateLargeSessionModel(b, base, tea.MouseWheelMsg{X: 30, Y: 12, Button: tea.MouseWheelUp})
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				key := tea.KeyPressMsg{Code: 'a', Text: "a"}
				if i%2 == 1 {
					key = tea.KeyPressMsg{Code: tea.KeyBackspace}
				}
				m = updateLargeSessionModel(b, m, key)
			}
		})
	}
}
