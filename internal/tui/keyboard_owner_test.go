package tui

// Table tests for the overlay keyboard-owner machine (derived, no writes).
// Covers every (owner x event) cell plus derive priority for singles and all
// pairs, mirroring the statusline/turn/prompt table style.

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

func allKeyboardOwners() []KeyboardOwner {
	return []KeyboardOwner{
		KeyboardBase, KeyboardSetup, KeyboardSTTKey, KeyboardTerminalAttach,
		KeyboardRename, KeyboardStatuslinePending, KeyboardHelp, KeyboardLeaderHelp,
		KeyboardPeerApproval, KeyboardPermissionTyping, KeyboardPermission,
		KeyboardAskUser, KeyboardSpecReview, KeyboardMCPAddWizard, KeyboardMCPManager,
		KeyboardPicker, KeyboardPaletteFile, KeyboardPaletteCommand,
		KeyboardLeaderPending, KeyboardDetail,
	}
}

func allKeyboardEvents() []KeyboardEvent {
	return []KeyboardEvent{
		KeyboardEvEsc, KeyboardEvEnter, KeyboardEvRune,
		KeyboardEvCtrlX, KeyboardEvCtrlC, KeyboardEvOther,
	}
}

func TestKeyboardOwnerTableAllCombinations(t *testing.T) {
	expected := map[KeyboardOwner]map[KeyboardEvent]bool{
		KeyboardBase:              {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardSetup:             {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardSTTKey:            {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardTerminalAttach:    {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardRename:            {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardStatuslinePending: {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardHelp:              {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardLeaderHelp:        {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPeerApproval:      {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPermissionTyping:  {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPermission:        {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardAskUser:           {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardSpecReview:        {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardMCPAddWizard:      {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardMCPManager:        {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPicker:            {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPaletteFile:       {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: false, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardPaletteCommand:    {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: false, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardLeaderPending:     {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: true, KeyboardEvCtrlX: true, KeyboardEvCtrlC: true, KeyboardEvOther: true},
		KeyboardDetail:            {KeyboardEvEsc: true, KeyboardEvEnter: true, KeyboardEvRune: false, KeyboardEvCtrlX: false, KeyboardEvCtrlC: true, KeyboardEvOther: false},
	}
	for _, owner := range allKeyboardOwners() {
		for _, ev := range allKeyboardEvents() {
			want := expected[owner][ev]
			if got := keyboardEventLegal(owner, ev); got != want {
				t.Errorf("owner %s event %s: legal=%v want %v", owner, ev, got, want)
			}
		}
		if owner.String() == "unknown" {
			t.Errorf("owner %d has no String", int(owner))
		}
	}
	for _, ev := range allKeyboardEvents() {
		if ev.String() == "unknown" {
			t.Errorf("event %d has no String", int(ev))
		}
	}
	// Spot-check the refusing checker (never panics): Detail must refuse a
	// rune, Base must accept it.
	m := newModel(context.Background(), Options{})
	if err := m.checkKeyboardEvent(KeyboardEvEsc); err != nil {
		t.Fatalf("base esc must be legal: %v", err)
	}
	m.runDetailsOpen = true
	if err := m.checkKeyboardEvent(KeyboardEvRune); err == nil {
		t.Fatal("detail rune must be illegal (falls through to composer)")
	}
	if err := m.checkKeyboardEvent(KeyboardEvEsc); err != nil {
		t.Fatalf("detail esc must be legal: %v", err)
	}
}

// TestKeyboardOwnerDeriveSingles pins the derive priority for one overlay at
// a time: each overlay maps to its owner, empty maps to base.
func TestKeyboardOwnerDeriveSingles(t *testing.T) {
	newBase := func(t *testing.T) model {
		t.Helper()
		return newModel(context.Background(), Options{})
	}
	cases := []struct {
		name string
		set  func(m model) model
		want KeyboardOwner
	}{
		{"base", func(m model) model { return m }, KeyboardBase},
		{"setup", func(m model) model { m.setup.visible = true; return m }, KeyboardSetup},
		{"stt", func(m model) model { m.sttKeyPrompt = &sttKeyPromptState{}; return m }, KeyboardSTTKey},
		{"attach", func(m model) model { m.terminalAttach = &terminalAttachState{}; return m }, KeyboardTerminalAttach},
		{"rename", func(m model) model { m.renamePrompt = &sessionRenamePrompt{}; return m }, KeyboardRename},
		{"statusPending", func(m model) model { m.statuslinePending = &statuslinePendingApply{}; return m }, KeyboardStatuslinePending},
		{"help", func(m model) model { m.helpOverlay = true; return m }, KeyboardHelp},
		{"leaderHelp", func(m model) model { m.leaderHelpOverlay = true; return m }, KeyboardLeaderHelp},
		{"peer", func(m model) model {
			held := peermsg.InboundMessage{ID: "x", RequiresApproval: true}
			m.peerPendingApproval = &held
			m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "peer-x"}}
			return m
		}, KeyboardPeerApproval},
		{"permTyping", func(m model) model {
			m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "t"}, typing: true}
			return m
		}, KeyboardPermissionTyping},
		{"perm", func(m model) model {
			m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "t"}}
			return m
		}, KeyboardPermission},
		{"ask", func(m model) model {
			m.pendingAskUser = &pendingAskUserPrompt{request: agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q?"}}}, answer: func([]string) {}, states: newAskUserStates([]agent.AskUserQuestion{{Question: "q?"}})}
			return m
		}, KeyboardAskUser},
		{"spec", func(m model) model { m.pendingSpecReview = &pendingSpecReviewPrompt{}; return m }, KeyboardSpecReview},
		{"mcpAdd", func(m model) model { m.mcpAddWizard = &mcpAddWizardState{}; return m }, KeyboardMCPAddWizard},
		{"mcp", func(m model) model { m.mcpManager = &mcpManagerState{}; return m }, KeyboardMCPManager},
		{"picker", func(m model) model { m.picker = &commandPicker{}; return m }, KeyboardPicker},
		{"paletteFile", func(m model) model { m.filePaletteOpen = true; return m }, KeyboardPaletteFile},
		{"paletteCommand", func(m model) model { m.commandPaletteOpen = true; return m }, KeyboardPaletteCommand},
		{"leader", func(m model) model { m.leaderPending = true; return m }, KeyboardLeaderPending},
		{"detailRun", func(m model) model { m.runDetailsOpen = true; return m }, KeyboardDetail},
		{"detailTranscript", func(m model) model { m.transcriptDetailed = true; return m }, KeyboardDetail},
		{"detailFile", func(m model) model { m.fileView.active = true; return m }, KeyboardDetail},
		{"detailSubchat", func(m model) model { m.subchat.active = true; return m }, KeyboardDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.set(newBase(t))
			if got := m.keyboardOwner(); got != tc.want {
				t.Fatalf("derive=%s want %s", got, tc.want)
			}
		})
	}
}

// TestKeyboardOwnerDerivePairs covers at least all pairs among the main
// overlays: the earlier in Update priority wins. Builders mirror the singles
// above; the matrix asserts the topmost wins for every i<j and j<i.
func TestKeyboardOwnerDerivePairs(t *testing.T) {
	type named struct {
		name string
		set  func(m *model)
		want KeyboardOwner
	}
	all := []named{
		{"setup", func(m *model) { m.setup.visible = true }, KeyboardSetup},
		{"stt", func(m *model) { m.sttKeyPrompt = &sttKeyPromptState{} }, KeyboardSTTKey},
		{"attach", func(m *model) { m.terminalAttach = &terminalAttachState{} }, KeyboardTerminalAttach},
		{"rename", func(m *model) { m.renamePrompt = &sessionRenamePrompt{} }, KeyboardRename},
		{"statusPending", func(m *model) { m.statuslinePending = &statuslinePendingApply{} }, KeyboardStatuslinePending},
		{"help", func(m *model) { m.helpOverlay = true }, KeyboardHelp},
		{"leaderHelp", func(m *model) { m.leaderHelpOverlay = true }, KeyboardLeaderHelp},
		{"perm", func(m *model) {
			m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "t"}}
		}, KeyboardPermission},
		{"ask", func(m *model) {
			m.pendingAskUser = &pendingAskUserPrompt{request: agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q?"}}}, answer: func([]string) {}, states: newAskUserStates([]agent.AskUserQuestion{{Question: "q?"}})}
		}, KeyboardAskUser},
		{"spec", func(m *model) { m.pendingSpecReview = &pendingSpecReviewPrompt{} }, KeyboardSpecReview},
		{"mcpAdd", func(m *model) { m.mcpAddWizard = &mcpAddWizardState{} }, KeyboardMCPAddWizard},
		{"mcp", func(m *model) { m.mcpManager = &mcpManagerState{} }, KeyboardMCPManager},
		{"picker", func(m *model) { m.picker = &commandPicker{} }, KeyboardPicker},
		{"paletteFile", func(m *model) { m.filePaletteOpen = true }, KeyboardPaletteFile},
		{"paletteCommand", func(m *model) { m.commandPaletteOpen = true }, KeyboardPaletteCommand},
		{"leader", func(m *model) { m.leaderPending = true }, KeyboardLeaderPending},
		{"detail", func(m *model) { m.runDetailsOpen = true }, KeyboardDetail},
	}
	for i := range all {
		for j := range all {
			if i == j {
				continue
			}
			m := newModel(context.Background(), Options{})
			all[i].set(&m)
			all[j].set(&m)
			// Earlier in the slice wins (slice is in priority order).
			want := all[i].want
			if j < i {
				want = all[j].want
			}
			// Palettes are exclusive via typing, but a forced both-true picks
			// file (recompute priority). Fix the expectation for that pair.
			if (all[i].name == "paletteFile" && all[j].name == "paletteCommand") ||
				(all[i].name == "paletteCommand" && all[j].name == "paletteFile") {
				want = KeyboardPaletteFile
			}
			if got := m.keyboardOwner(); got != want {
				t.Fatalf("pair %s+%s: derive=%s want %s", all[i].name, all[j].name, got, want)
			}
		}
	}
}
