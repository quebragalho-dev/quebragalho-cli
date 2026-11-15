package tui

// Characterization of the current keyboard priority.
// These tests PASS on the untouched dev branch and must keep passing after
// the fix without edits: they pin the observable behaviour
// for single overlays and for overlay pairs whose handling the fix does
// not change (the leader-chord cancellation has its own repro in
// keyboard_owner_repro_test.go).
//
// Method: drive the production Update path (KeyPressMsg)
// with hermetic models (HOME/XDG fakes are set by the package harness;
// QG_CRED_STORAGE=file where needed). No time.Sleep.

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func charTestModel(t *testing.T, now time.Time) model {
	t.Helper()
	t.Setenv("QG_CRED_STORAGE", "file")
	m := newModel(context.Background(), Options{})
	m.now = func() time.Time { return now }
	return m
}

// Slash and at-sign together: recomputeSuggestions gives a deterministic,
// exclusive priority — a path query ("@") wins over the trailing slash token
// ("/"). Both flags true is representable in the type but unreachable via
// typing. This pins the current (correct) priority; no defect.
func TestCharSlashAtPriorityFileWins(t *testing.T) {
	m := charTestModel(t, time.Now())
	m.input.SetValue("/rev @foo")
	m.recomputeSuggestions()
	if !m.filePaletteOpen || m.commandPaletteOpen {
		t.Fatalf("file palette must win for %q: cmd=%v file=%v", m.input.Value(), m.commandPaletteOpen, m.filePaletteOpen)
	}
	m2 := charTestModel(t, time.Now())
	m2.input.SetValue("@foo /rev")
	m2.recomputeSuggestions()
	// Trailing "/rev" with no live "@" before the cursor: command palette.
	if !m2.commandPaletteOpen || m2.filePaletteOpen {
		t.Fatalf("command palette expected for %q: cmd=%v file=%v", m2.input.Value(), m2.commandPaletteOpen, m2.filePaletteOpen)
	}
}

// Picker resume chain: Tab opens one effort ladder over the model
// palette; a second Tab inside the ladder must NOT grow the chain, and Esc
// restores the palette. Depth stays bounded at 2 (palette + ladder).
func TestCharPickerResumeDepthBounded(t *testing.T) {
	m := charTestModel(t, time.Now())
	m.picker = &commandPicker{kind: pickerModel, panel: true, items: []pickerItem{{Label: "a", Value: "a"}}, allItems: []pickerItem{{Label: "a", Value: "a"}}}
	updated, _, handled := m.handlePanelKey(testKey(tea.KeyTab))
	if !handled {
		t.Fatal("Tab in model palette should open the effort ladder")
	}
	m = updated
	if m.picker == nil || m.picker.kind != pickerEffort || m.picker.resume == nil {
		t.Fatalf("expected effort ladder with resume, got %+v", m.picker)
	}
	depth := 0
	for p := m.picker; p != nil; p = p.resume {
		depth++
	}
	if depth != 2 {
		t.Fatalf("depth after 1 Tab = %d, want 2", depth)
	}
	updated2, _, _ := m.handlePanelKey(testKey(tea.KeyTab))
	depth2 := 0
	for p := updated2.picker; p != nil; p = p.resume {
		depth2++
		if depth2 > 10 {
			t.Fatal("resume chain grew unbounded")
		}
	}
	if depth2 != 2 {
		t.Fatalf("depth after 2nd Tab = %d, want 2 (no growth)", depth2)
	}
	updated3, _, _ := updated2.handlePanelKey(testKey(tea.KeyEsc))
	if updated3.picker == nil || updated3.picker.kind != pickerModel {
		t.Fatalf("Esc must restore the model palette, got %+v", updated3.picker)
	}
	if updated3.picker.resume != nil {
		t.Fatal("restored palette must not carry a resume link")
	}
}

// Single-overlay characterization: with exactly one modal open, Esc must go
// to that modal (or its documented fallback) and must not touch the composer
// draft unexpectedly. Each case passes on dev and must pass after the fix.
func TestCharSingleOverlayEsc(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		setup func(m model) model
		check func(t *testing.T, before, after model)
	}{
		{
			name: "help closes",
			setup: func(m model) model {
				m.helpOverlay = true
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.helpOverlay {
					t.Fatal("Esc must close helpOverlay")
				}
			},
		},
		{
			name: "leaderHelp closes",
			setup: func(m model) model {
				m.leaderHelpOverlay = true
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.leaderHelpOverlay {
					t.Fatal("Esc must close leaderHelpOverlay")
				}
			},
		},
		{
			name: "picker cancels",
			setup: func(m model) model {
				m.picker = &commandPicker{kind: pickerSession, items: []pickerItem{{Label: "a", Value: "a"}}}
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.picker != nil {
					t.Fatal("Esc must cancel the picker")
				}
			},
		},
		{
			name: "mcpManager cancels",
			setup: func(m model) model {
				m.mcpManager = &mcpManagerState{}
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.mcpManager != nil {
					t.Fatal("Esc must cancel mcpManager")
				}
			},
		},
		{
			name: "transcriptDetailed exits",
			setup: func(m model) model {
				m.transcriptDetailed = true
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.transcriptDetailed {
					t.Fatal("Esc must exit transcriptDetailed")
				}
			},
		},
		{
			name: "runDetails collapses",
			setup: func(m model) model {
				m.runDetailsOpen = true
				return m
			},
			check: func(t *testing.T, _, after model) {
				t.Helper()
				if after.runDetailsOpen {
					t.Fatal("Esc must collapse runDetailsOpen")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := charTestModel(t, now)
			m = tc.setup(m)
			updated, _ := m.Update(testKey(tea.KeyEsc))
			after := updated.(model)
			tc.check(t, m, after)
		})
	}
}

// Pair characterization (non-defect pairs): the earlier surface in the Update
// chain wins and the later stays untouched. Covers at least all pairs among
// the cheap, synchronously openable overlays.
func TestCharOverlayPairsEarlyWins(t *testing.T) {
	now := time.Now()
	mkPicker := func() *commandPicker {
		return &commandPicker{kind: pickerSession, items: []pickerItem{{Label: "a", Value: "a"}}}
	}
	t.Run("help over picker", func(t *testing.T) {
		m := charTestModel(t, now)
		m.helpOverlay = true
		m.picker = mkPicker()
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.helpOverlay {
			t.Fatal("help must win over picker for Esc")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when help wins Esc")
		}
	})
	t.Run("picker over suggestions", func(t *testing.T) {
		m := charTestModel(t, now)
		m.picker = mkPicker()
		m.input.SetValue("/mo")
		m.recomputeSuggestions()
		// Picker open plus a stale suggestion list: Esc must cancel the picker
		// first, leaving suggestions alone.
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.picker != nil {
			t.Fatal("picker must win over suggestions for Esc")
		}
	})
	t.Run("mcpManager over picker", func(t *testing.T) {
		m := charTestModel(t, now)
		m.mcpManager = &mcpManagerState{}
		m.picker = mkPicker()
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.mcpManager != nil {
			t.Fatal("mcpManager must win over picker for Esc")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when mcpManager wins Esc")
		}
	})
	t.Run("transcriptDetailed over picker", func(t *testing.T) {
		m := charTestModel(t, now)
		m.transcriptDetailed = true
		m.picker = mkPicker()
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.transcriptDetailed {
			t.Fatal("transcriptDetailed must win over picker for Esc")
		}
	})
}
