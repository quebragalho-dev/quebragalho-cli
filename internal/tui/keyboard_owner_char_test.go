package tui

// Characterization of the current keyboard-owner priority.
// These tests PASS on the untouched dev branch and must keep passing after
// the fix without edits: they pin the observable behaviour for single
// overlays and for overlay pairs whose handling the fix does not change
// (the leader-chord cancellation has its own failing-then-passing repro in
// keyboard_owner_repro_test.go).
//
// Method: drive the production Update path (KeyPressMsg) with hermetic
// models (HOME/XDG fakes are set by the package harness;
// QG_CRED_STORAGE=file where needed). No time.Sleep.

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
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

// Real key-dispatch priority, read off the Update KeyPressMsg chain in
// model.go (see the dispatch-order comment there). Each case mounts one
// surface, sends Esc and separately the letter "z" through the real Update,
// and asserts which surface acted and which stayed intact. The letter "z" is
// the neutral probe: no overlay binds it (permission hotkeys are a/r/s/p/y/d/n,
// spec review uses a/r/e/c), the leader finds no slash for it, and it is not
// a help/leader-help dismiss key. Cases mounted only by writing fields carry
// "synthetic" in the name; the rest use production openers (keys, agent
// messages, typing, the leader slash). Expectations were written by hand from
// the behavior observed on the untouched dev branch; the prompt-cancellation
// fix touches none of these paths, so this test passes before and after
// without edits. (Leader-chord races against opening prompts live in the
// repro file instead.)
func TestOverlayKeyPrioritySingles(t *testing.T) {
	type singleCase struct {
		name       string
		mount      func(t *testing.T) model
		present    func(m model) bool
		escGone    bool   // present(after Esc) == false
		zPresent   bool   // present(after "z") == true
		wantZInput string // composer value expected after "z"
	}
	noopDecide := func(agent.PermissionDecision) {}
	noopAnswer := func([]string) {}
	cases := []singleCase{
		{
			name:       "setup_synthetic",
			mount:      func(t *testing.T) model { m := charTestModel(t, time.Now()); m.setup.visible = true; return m },
			present:    func(m model) bool { return m.setup.visible },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "stt_key_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.sttKeyPrompt = &sttKeyPromptState{}
				return m
			},
			present:    func(m model) bool { return m.sttKeyPrompt != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "terminal_attach_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.terminalAttach = &terminalAttachState{}
				return m
			},
			present:    func(m model) bool { return m.terminalAttach != nil },
			escGone:    true,
			zPresent:   false, // no live PTY session in unit tests, so the keystroke closes the overlay instead of reaching a process
			wantZInput: "",
		},
		{
			name: "rename_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.renamePrompt = &sessionRenamePrompt{}
				return m
			},
			present:    func(m model) bool { return m.renamePrompt != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "z",
		},
		{
			name: "statusline_preview_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.statuslinePending = &statuslinePendingApply{}
				return m
			},
			present:    func(m model) bool { return m.statuslinePending != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "help",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				updated, _ := m.Update(testKeyText("?"))
				return updated.(model)
			},
			present:    func(m model) bool { return m.helpOverlay },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "leader_help",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				updated, _ := m.Update(testKeyCtrl('x'))
				m = updated.(model)
				updated, _ = m.Update(testKeyText("?"))
				return updated.(model)
			},
			present:    func(m model) bool { return m.leaderHelpOverlay },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "peer_approval",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				next, _ := m.handlePeerMessage(peermsg.InboundMessage{
					ID:               "peer-1",
					From:             peermsg.Peer{Identity: peermsg.Identity{Name: "Peer"}, Ref: "11223344"},
					Body:             "Continue.",
					RequiresApproval: true,
					HoldCause:        peermsg.HoldCauseExplicit,
				})
				return next
			},
			present:    func(m model) bool { return m.pendingPermission != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "permission",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.activeRunID = 7
				updated, _ := m.Update(permissionRequestMsg{runID: 7, request: agent.PermissionRequest{ToolCallID: "tc1", ToolName: "bash", Action: agent.PermissionActionPrompt}, decide: noopDecide})
				return updated.(model)
			},
			present:    func(m model) bool { return m.pendingPermission != nil },
			escGone:    false, // Esc is a no-op on a tool prompt; only hotkeys/Enter resolve it
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "permission_typing_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "tc1", ToolName: "bash"}, typing: true, savedDraft: "draft"}
				return m
			},
			present:    func(m model) bool { return m.pendingPermission != nil && m.pendingPermission.typing },
			escGone:    true, // Esc steps back to the option list, restoring the draft
			zPresent:   true,
			wantZInput: "z",
		},
		{
			name: "ask_user",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.activeRunID = 7
				req := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "Pick one?"}}}
				updated, _ := m.Update(askUserRequestMsg{runID: 7, request: req, answer: noopAnswer})
				return updated.(model)
			},
			present:    func(m model) bool { return m.pendingAskUser != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "z", // printable flips the picker question into free text
		},
		{
			name: "spec_review_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				review := pendingSpecReviewPrompt{SpecID: "s1", SpecTitle: "t", SpecFilePath: "p", RelativePath: "r", DraftSessionID: "d"}
				if err := m.promptTransition(PromptEvOpenSpecReview, promptArgs{spec: &review}); err != nil {
					t.Fatalf("open spec review: %v", err)
				}
				return m
			},
			present:    func(m model) bool { return m.pendingSpecReview != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "mcp_add_wizard_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.mcpAddWizard = &mcpAddWizardState{}
				return m
			},
			present:    func(m model) bool { return m.mcpAddWizard != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "mcp_manager_synthetic",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				m.mcpManager = &mcpManagerState{}
				return m
			},
			present:    func(m model) bool { return m.mcpManager != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "picker",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				updated, _ := m.Update(testKeyCtrl('x'))
				m = updated.(model)
				updated, _ = m.Update(testKeyText("m"))
				m = updated.(model)
				if m.picker == nil {
					t.Fatal("leader slash must open the picker")
				}
				return m
			},
			present:    func(m model) bool { return m.picker != nil },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name: "command_palette",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				return typeRunes(t, m, "/mo")
			},
			present:    func(m model) bool { return m.suggestionsActive() },
			escGone:    true,
			zPresent:   true,
			wantZInput: "/moz",
		},
		{
			name: "file_palette",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				return typeRunes(t, m, "@f")
			},
			present:    func(m model) bool { return m.suggestionsActive() },
			escGone:    true,
			zPresent:   true,
			wantZInput: "@fz",
		},
		{
			name: "leader_chord",
			mount: func(t *testing.T) model {
				m := charTestModel(t, time.Now())
				updated, _ := m.Update(testKeyCtrl('x'))
				return updated.(model)
			},
			present:    func(m model) bool { return m.leaderPending },
			escGone:    true,
			zPresent:   false, // unmapped follow-up resolves (clears) the chord
			wantZInput: "",
		},
		{
			name:       "detail_transcript_synthetic",
			mount:      func(t *testing.T) model { m := charTestModel(t, time.Now()); m.transcriptDetailed = true; return m },
			present:    func(m model) bool { return m.transcriptDetailed },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name:       "detail_run_synthetic",
			mount:      func(t *testing.T) model { m := charTestModel(t, time.Now()); m.runDetailsOpen = true; return m },
			present:    func(m model) bool { return m.runDetailsOpen },
			escGone:    true,
			zPresent:   true,
			wantZInput: "",
		},
		{
			name:       "detail_file_synthetic",
			mount:      func(t *testing.T) model { m := charTestModel(t, time.Now()); m.fileView.active = true; return m },
			present:    func(m model) bool { return m.fileView.active },
			escGone:    true,
			zPresent:   true,
			wantZInput: "z", // plain runes fall through to the composer
		},
		{
			name:       "detail_subchat_synthetic",
			mount:      func(t *testing.T) model { m := charTestModel(t, time.Now()); m.subchat.active = true; return m },
			present:    func(m model) bool { return m.subchat.active },
			escGone:    true,
			zPresent:   true,
			wantZInput: "z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/esc", func(t *testing.T) {
			m := tc.mount(t)
			if !tc.present(m) {
				t.Fatalf("mount must open the surface")
			}
			updated, _ := m.Update(testKey(tea.KeyEsc))
			after := updated.(model)
			if tc.present(after) == tc.escGone {
				if tc.escGone {
					t.Fatalf("Esc must close the surface")
				}
				t.Fatalf("Esc must leave the surface intact")
			}
		})
		t.Run(tc.name+"/letter", func(t *testing.T) {
			m := tc.mount(t)
			updated, _ := m.Update(testKeyText("z"))
			after := updated.(model)
			if after.composerValue() != tc.wantZInput {
				t.Fatalf("composer after z = %q, want %q", after.composerValue(), tc.wantZInput)
			}
			if tc.present(after) != tc.zPresent {
				t.Fatalf("surface present after z = %v, want %v", tc.present(after), tc.zPresent)
			}
		})
	}
}

// Key-dispatch priority for overlay pairs, through the real Update. Each
// subtest mounts two surfaces, sends Esc and separately the letter "z", and
// asserts which surface acted while the other stayed intact. Mounting only by
// writing fields is marked "synthetic" in the subtest name; the other mounts
// use production openers (keys, agent messages, typing, the leader slash).
// Expectations were written by hand from the behavior observed on the
// untouched dev branch; the prompt-cancellation fix touches none of these
// paths, so this test passes before and after without edits.
func TestOverlayKeyPriorityPairs(t *testing.T) {
	mkPicker := func() *commandPicker {
		return &commandPicker{kind: pickerSession, items: []pickerItem{{Label: "a", Value: "a"}}}
	}
	openPermission := func(t *testing.T, m model) model {
		t.Helper()
		m.activeRunID = 7
		updated, _ := m.Update(permissionRequestMsg{runID: 7, request: agent.PermissionRequest{ToolCallID: "tc1", ToolName: "bash", Action: agent.PermissionActionPrompt}, decide: func(agent.PermissionDecision) {}})
		m = updated.(model)
		if m.pendingPermission == nil {
			t.Fatal("permission prompt must be live")
		}
		return m
	}
	armLeader := func(t *testing.T, m model) model {
		t.Helper()
		updated, _ := m.Update(testKeyCtrl('x'))
		m = updated.(model)
		if !m.leaderPending {
			t.Fatal("Ctrl+X must arm leaderPending")
		}
		return m
	}

	t.Run("leader_over_command_palette", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m = typeRunes(t, m, "/mo")
			if !m.suggestionsActive() {
				t.Fatal("command palette must be live")
			}
			return armLeader(t, m)
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.leaderPending {
			t.Fatal("Esc must resolve the leader chord")
		}
		if !after.suggestionsActive() {
			t.Fatal("palette must stay open when the leader wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.leaderPending {
			t.Fatal("unmapped follow-up must resolve the leader chord")
		}
		if !after.suggestionsActive() {
			t.Fatal("palette must stay open when the leader wins the follow-up key")
		}
		if after.composerValue() != "/mo" {
			t.Fatalf("follow-up key must not leak into the composer, got %q", after.composerValue())
		}
	})

	t.Run("leader_over_detail_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m = armLeader(t, m)
			m.transcriptDetailed = true
			return m
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.leaderPending {
			t.Fatal("Esc must resolve the leader chord")
		}
		if !after.transcriptDetailed {
			t.Fatal("detail view must stay open when the leader wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.leaderPending {
			t.Fatal("unmapped follow-up must resolve the leader chord")
		}
		if !after.transcriptDetailed {
			t.Fatal("detail view must stay open when the leader wins the follow-up key")
		}
		if after.composerValue() != "" {
			t.Fatalf("follow-up key must not leak into the composer, got %q", after.composerValue())
		}
	})

	t.Run("help_over_picker_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			updated, _ := m.Update(testKeyText("?"))
			m = updated.(model)
			if !m.helpOverlay {
				t.Fatal("help must be live")
			}
			m.picker = mkPicker()
			return m
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.helpOverlay {
			t.Fatal("Esc must close help")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when help wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if !after.helpOverlay {
			t.Fatal("help must swallow the letter and stay open")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when help wins the letter")
		}
	})

	t.Run("picker_over_palette_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m.picker = mkPicker()
			m.input.SetValue("/mo")
			m.recomputeSuggestions()
			if !m.suggestionsActive() {
				t.Fatal("palette must be live")
			}
			return m
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.picker != nil {
			t.Fatal("Esc must cancel the picker first")
		}
		if !after.suggestionsActive() {
			t.Fatal("palette must stay live when the picker wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.picker == nil || after.picker.query != "z" {
			t.Fatalf("letter must reach the picker query, got %+v", after.picker)
		}
		if !after.suggestionsActive() {
			t.Fatal("palette must stay live when the picker wins the letter")
		}
		if after.composerValue() != "/mo" {
			t.Fatalf("letter must not leak into the composer, got %q", after.composerValue())
		}
	})

	t.Run("tool_permission_with_picker_synthetic", func(t *testing.T) {
		// Observed z-order: Esc reaches the viewport picker before the
		// footer permission card (a tool prompt ignores Esc), while a
		// printable letter is swallowed by the card.
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m.picker = mkPicker()
			return openPermission(t, m)
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.picker != nil {
			t.Fatal("Esc must cancel the picker first (tool prompts ignore Esc)")
		}
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live when Esc cancels the picker above it")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live on an unbound letter")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open: the letter never reaches it while the card owns the key")
		}
		if after.composerValue() != "" {
			t.Fatalf("letter must not leak into the composer, got %q", after.composerValue())
		}
	})

	t.Run("mcp_manager_over_picker_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m.mcpManager = &mcpManagerState{}
			m.picker = mkPicker()
			return m
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.mcpManager != nil {
			t.Fatal("Esc must close the MCP manager")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when the manager wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.mcpManager == nil {
			t.Fatal("manager must stay open on a query letter")
		}
		if after.picker == nil {
			t.Fatal("picker must stay open when the manager wins the letter")
		}
	})

	t.Run("statusline_preview_over_permission_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m.statuslinePending = &statuslinePendingApply{}
			return openPermission(t, m)
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.statuslinePending != nil {
			t.Fatal("Esc must discard the statusline proposal")
		}
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live when the preview wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.statuslinePending == nil {
			t.Fatal("preview must swallow the letter and stay live")
		}
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live when the preview wins the letter")
		}
		if after.composerValue() != "" {
			t.Fatalf("letter must not leak into the composer, got %q", after.composerValue())
		}
	})

	t.Run("terminal_attach_over_permission_synthetic", func(t *testing.T) {
		mount := func(t *testing.T) model {
			m := charTestModel(t, time.Now())
			m.terminalAttach = &terminalAttachState{}
			return openPermission(t, m)
		}
		m := mount(t)
		updated, _ := m.Update(testKey(tea.KeyEsc))
		after := updated.(model)
		if after.terminalAttach != nil {
			t.Fatal("Esc must detach the terminal overlay")
		}
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live when attach wins Esc")
		}
		m = mount(t)
		updated, _ = m.Update(testKeyText("z"))
		after = updated.(model)
		if after.pendingPermission == nil {
			t.Fatal("permission card must stay live: the letter never reaches it while attach owns the key")
		}
		if after.composerValue() != "" {
			t.Fatalf("letter must not leak into the composer, got %q", after.composerValue())
		}
	})
}
