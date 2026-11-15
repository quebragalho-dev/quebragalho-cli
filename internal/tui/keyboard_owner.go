package tui

// KeyboardOwner names the surface that owns the next keystroke.
//
//	Text diagram:
//	  Base -> Setup -> STTKey -> TerminalAttach -> Rename -> StatuslinePending
//	  Setup/STT/Attach/Rename/StatusPending own every key (early returns).
//	  Help/LeaderHelp own every key (scrim, swallow rest).
//	  Peer/Permission(Typing)/AskUser/SpecReview own every key (footer modal).
//	  MCPAdd/MCPManager own every key (manager overlay).
//	  Picker owns panel keys (Esc/Tab/Enter/arrows) and query runes.
//	  PaletteFile (@"...") wins over PaletteCommand ("/...") — recompute checks
//	    the path query first, so the two flags never co-occur via typing.
//	  LeaderPending owns every key except Ctrl+C (clears, then exit/cancel)
//	    until the follow-up, timeout, or Esc; a prompt that opens mid-chord
//	    cancels it (S2 fix), so Leader never coexists with Peer/Permission.
//	  Detail (runDetails/transcriptDetailed/fileView/subchat/planDetail) owns
//	    Esc (exit/collapse) and swallows Enter, but lets runes fall through
//	    to the composer (fileView d/f only on empty composer).
//	  Base owns everything else (composer + suggestions).
//
// Derived only: keyboardOwner() reads the live fields in Update priority
// order. No field is added, no writer exists; checkKeyboardEvent only
// refuses illegal (owner x key) pairs keeping state, never panicking.
// loopBusy consults the Setup case (S1 fix); all other gates keep their
// current ifs — equivalence beyond Setup was not proven, see report.

import (
	"fmt"
)

type KeyboardOwner int

const (
	KeyboardBase KeyboardOwner = iota
	KeyboardSetup
	KeyboardSTTKey
	KeyboardTerminalAttach
	KeyboardRename
	KeyboardStatuslinePending
	KeyboardHelp
	KeyboardLeaderHelp
	KeyboardPeerApproval
	KeyboardPermissionTyping
	KeyboardPermission
	KeyboardAskUser
	KeyboardSpecReview
	KeyboardMCPAddWizard
	KeyboardMCPManager
	KeyboardPicker
	KeyboardPaletteFile
	KeyboardPaletteCommand
	KeyboardLeaderPending
	KeyboardDetail
)

func (o KeyboardOwner) String() string {
	switch o {
	case KeyboardBase:
		return "base"
	case KeyboardSetup:
		return "setup"
	case KeyboardSTTKey:
		return "sttKey"
	case KeyboardTerminalAttach:
		return "terminalAttach"
	case KeyboardRename:
		return "rename"
	case KeyboardStatuslinePending:
		return "statuslinePending"
	case KeyboardHelp:
		return "help"
	case KeyboardLeaderHelp:
		return "leaderHelp"
	case KeyboardPeerApproval:
		return "peerApproval"
	case KeyboardPermissionTyping:
		return "permissionTyping"
	case KeyboardPermission:
		return "permission"
	case KeyboardAskUser:
		return "askUser"
	case KeyboardSpecReview:
		return "specReview"
	case KeyboardMCPAddWizard:
		return "mcpAddWizard"
	case KeyboardMCPManager:
		return "mcpManager"
	case KeyboardPicker:
		return "picker"
	case KeyboardPaletteFile:
		return "paletteFile"
	case KeyboardPaletteCommand:
		return "paletteCommand"
	case KeyboardLeaderPending:
		return "leaderPending"
	case KeyboardDetail:
		return "detail"
	default:
		return "unknown"
	}
}

type KeyboardEvent int

const (
	KeyboardEvEsc KeyboardEvent = iota
	KeyboardEvEnter
	KeyboardEvRune
	KeyboardEvCtrlX
	KeyboardEvCtrlC
	KeyboardEvOther
)

func (e KeyboardEvent) String() string {
	switch e {
	case KeyboardEvEsc:
		return "esc"
	case KeyboardEvEnter:
		return "enter"
	case KeyboardEvRune:
		return "rune"
	case KeyboardEvCtrlX:
		return "ctrlX"
	case KeyboardEvCtrlC:
		return "ctrlC"
	case KeyboardEvOther:
		return "other"
	default:
		return "unknown"
	}
}

// keyboardLegal is the single table of (owner x key) handling: true means the
// owner consumes the key (swallow or act), false means it must fall through.
// Full modals consume everything; palettes let Ctrl+X through so the leader
// can arm (canArmLeader stays true with a palette open); Detail lets runes
// and Ctrl+X through (fileView d/f only on empty composer; Ctrl+X arms).
var keyboardLegal = map[KeyboardOwner]map[KeyboardEvent]bool{
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

func keyboardEventLegal(owner KeyboardOwner, ev KeyboardEvent) bool {
	return keyboardLegal[owner][ev]
}

// keyboardOwner derives the keyboard owner from the live fields in exactly
// the Update/handleKey priority order: early fullscreen modals first, then
// help, then prompts (peer first, like promptPhase), then MCP, picker,
// palettes (file wins over command, like recomputeSuggestions), leader, and
// finally the detail/drill-in views. Base is the composer.
func (m model) keyboardOwner() KeyboardOwner {
	if m.setup.visible {
		return KeyboardSetup
	}
	if m.sttKeyPrompt != nil {
		return KeyboardSTTKey
	}
	if m.terminalAttach != nil {
		return KeyboardTerminalAttach
	}
	if m.renamePrompt != nil {
		return KeyboardRename
	}
	if m.statuslinePending != nil {
		return KeyboardStatuslinePending
	}
	if m.helpOverlay {
		return KeyboardHelp
	}
	if m.leaderHelpOverlay {
		return KeyboardLeaderHelp
	}
	if m.peerPendingApproval != nil {
		return KeyboardPeerApproval
	}
	if m.pendingPermission != nil {
		if m.pendingPermission.typing {
			return KeyboardPermissionTyping
		}
		return KeyboardPermission
	}
	if m.pendingAskUser != nil {
		return KeyboardAskUser
	}
	if m.pendingSpecReview != nil {
		return KeyboardSpecReview
	}
	if m.mcpAddWizard != nil {
		return KeyboardMCPAddWizard
	}
	if m.mcpManager != nil {
		return KeyboardMCPManager
	}
	if m.picker != nil {
		return KeyboardPicker
	}
	if m.filePaletteOpen {
		return KeyboardPaletteFile
	}
	if m.commandPaletteOpen {
		return KeyboardPaletteCommand
	}
	if m.suggestionsActive() {
		if m.suggestionsAreFiles {
			return KeyboardPaletteFile
		}
		return KeyboardPaletteCommand
	}
	if m.leaderPending {
		return KeyboardLeaderPending
	}
	if m.runDetailsOpen || m.transcriptDetailed || m.fileView.active || m.subchat.active || m.planDetailOpen {
		return KeyboardDetail
	}
	return KeyboardBase
}

// checkKeyboardEvent refuses an illegal (owner x key) pair keeping state,
// never panicking. There are no writes by design (derived state); callers
// use it to document intent, not to gate production keys.
func (m model) checkKeyboardEvent(ev KeyboardEvent) error {
	if !keyboardEventLegal(m.keyboardOwner(), ev) {
		return fmt.Errorf("illegal keyboard event %s in %s", ev, m.keyboardOwner())
	}
	return nil
}
