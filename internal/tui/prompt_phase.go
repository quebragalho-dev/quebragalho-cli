package tui

// PromptPhase names the single blocking prompt the TUI can show.
//
//	Text diagram:
//	  None -> Permission -> PermissionTyping -> None (submit/cancel/cancelRun)
//	  None -> AskUser -> None (submit/esc/cancelRun)
//	  None -> SpecReview -> None (approve/reject/cancel; survives cancelRun)
//	  None -> PeerApproval -> None (decide/expire/release; cancelRun keeps queue)
//	Only one prompt may be live at a time; a second open while busy is illegal
//	and must be refused (the newcomer is resolved with Cancel, never overwrites).
//
// The live fields are the twins: pendingPermission.typing/savedDraft share the
// composer input, and peerPendingApproval pairs with a peer ToolCallID prompt.
// promptTransition is the single writer that fixes those twins together.

import (
	"fmt"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

type PromptPhase int

const (
	PromptNone PromptPhase = iota
	PromptPermission
	PromptPermissionTyping
	PromptAskUser
	PromptSpecReview
	PromptPeerApproval
)

func (p PromptPhase) String() string {
	switch p {
	case PromptNone:
		return "none"
	case PromptPermission:
		return "permission"
	case PromptPermissionTyping:
		return "permissionTyping"
	case PromptAskUser:
		return "askUser"
	case PromptSpecReview:
		return "specReview"
	case PromptPeerApproval:
		return "peerApproval"
	default:
		return "unknown"
	}
}

type PromptEvent int

const (
	PromptEvOpenPermission PromptEvent = iota
	PromptEvOpenAskUser
	PromptEvOpenSpecReview
	PromptEvOpenPeer
	PromptEvEnterTyping
	PromptEvCancelTyping
	PromptEvResolvePermission
	PromptEvResolveAskUser
	PromptEvResolveSpecReview
	PromptEvCancelAll
	PromptEvExpirePeer
	PromptEvReleasePeer
)

func (e PromptEvent) String() string {
	switch e {
	case PromptEvOpenPermission:
		return "openPermission"
	case PromptEvOpenAskUser:
		return "openAskUser"
	case PromptEvOpenSpecReview:
		return "openSpecReview"
	case PromptEvOpenPeer:
		return "openPeer"
	case PromptEvEnterTyping:
		return "enterTyping"
	case PromptEvCancelTyping:
		return "cancelTyping"
	case PromptEvResolvePermission:
		return "resolvePermission"
	case PromptEvResolveAskUser:
		return "resolveAskUser"
	case PromptEvResolveSpecReview:
		return "resolveSpecReview"
	case PromptEvCancelAll:
		return "cancelAll"
	case PromptEvExpirePeer:
		return "expirePeer"
	case PromptEvReleasePeer:
		return "releasePeer"
	default:
		return "unknown"
	}
}

// promptLegal is the single table of legal (phase x event) transitions.
var promptLegal = map[PromptPhase]map[PromptEvent]bool{
	PromptNone: {
		PromptEvOpenPermission: true,
		PromptEvOpenAskUser:    true,
		PromptEvOpenSpecReview: true,
		PromptEvOpenPeer:       true,
		PromptEvCancelAll:      true,
		PromptEvExpirePeer:     true,
		PromptEvReleasePeer:    true,
	},
	PromptPermission: {
		PromptEvEnterTyping:       true,
		PromptEvResolvePermission: true,
		PromptEvCancelAll:         true,
	},
	PromptPermissionTyping: {
		PromptEvCancelTyping:      true,
		PromptEvResolvePermission: true,
		PromptEvCancelAll:         true,
	},
	PromptAskUser: {
		PromptEvResolveAskUser: true,
		PromptEvCancelAll:      true,
	},
	PromptSpecReview: {
		PromptEvResolveSpecReview: true,
		PromptEvCancelAll:         true,
	},
	PromptPeerApproval: {
		PromptEvResolvePermission: true,
		PromptEvCancelAll:         true,
		PromptEvExpirePeer:        true,
		PromptEvReleasePeer:       true,
	},
}

func promptEventLegal(phase PromptPhase, ev PromptEvent) bool {
	return promptLegal[phase][ev]
}

// promptPhase derives the current blocking-prompt phase from the live fields.
// More than one pending* non-nil is an impossible split-brain: the first in
// priority wins here; the table forbids reaching it (second open is refused).
func (m model) promptPhase() PromptPhase {
	if m.peerPendingApproval != nil {
		return PromptPeerApproval
	}
	if m.pendingPermission != nil {
		if m.pendingPermission.typing {
			return PromptPermissionTyping
		}
		return PromptPermission
	}
	if m.pendingAskUser != nil {
		return PromptAskUser
	}
	if m.pendingSpecReview != nil {
		return PromptSpecReview
	}
	return PromptNone
}

// promptArgs carries the optional payload for a transition.
type promptArgs struct {
	permReq    *agent.PermissionRequest
	permDecide func(agent.PermissionDecision)
	askReq     *agent.AskUserRequest
	askAnswer  func([]string)
	askStates  []askUserAnswerState
	spec       *pendingSpecReviewPrompt
	peer       *peermsg.InboundMessage
	decision   permissionDecision
	reason     string
	answers    []string
	expireID   string
	released   *peermsg.InboundMessage
}

// promptTransition is the single point that writes the prompt fields. It
// validates (phase x event) against promptLegal, refuses illegal transitions
// with an error while keeping state, and fixes twin fields together:
// typing/savedDraft/input are zeroed/restored as a unit, and a peer expire
// only clears a prompt whose ToolCallID matches the peer message.
func (m *model) promptTransition(ev PromptEvent, a promptArgs) error {
	phase := m.promptPhase()
	if !promptEventLegal(phase, ev) {
		return fmt.Errorf("illegal prompt transition %s in %s", ev, phase)
	}
	switch ev {
	case PromptEvOpenPermission:
		if a.permReq == nil {
			return fmt.Errorf("openPermission without request")
		}
		m.pendingPermission = &pendingPermissionPrompt{request: *a.permReq, decide: a.permDecide}
	case PromptEvOpenAskUser:
		if a.askReq == nil {
			return fmt.Errorf("openAskUser without request")
		}
		states := a.askStates
		if states == nil {
			states = newAskUserStates(a.askReq.Questions)
		}
		m.pendingAskUser = &pendingAskUserPrompt{request: *a.askReq, answer: a.askAnswer, states: states}
	case PromptEvOpenSpecReview:
		if a.spec == nil {
			return fmt.Errorf("openSpecReview without review")
		}
		review := *a.spec
		m.pendingSpecReview = &review
	case PromptEvOpenPeer:
		if a.peer == nil {
			return fmt.Errorf("openPeer without message")
		}
		message := *a.peer
		m.peerPendingApproval = &message
		m.pendingPermission = &pendingPermissionPrompt{
			request: agent.PermissionRequest{
				ToolCallID: "peer-" + message.ID,
				ToolName:   peerPermissionToolName,
			},
			decide: a.permDecide,
		}
		// Fill the display-only fields the direct constructor sets; the
		// transition owns the twins, callers own presentation.
		if m.pendingPermission != nil && a.permReq != nil {
			m.pendingPermission.request = *a.permReq
			m.pendingPermission.decide = a.permDecide
		}
	case PromptEvEnterTyping:
		if m.pendingPermission == nil {
			return fmt.Errorf("enterTyping without prompt")
		}
		m.pendingPermission.typing = true
		m.pendingPermission.savedDraft = m.input.Value()
		m.input.SetValue("")
	case PromptEvCancelTyping:
		if m.pendingPermission == nil || !m.pendingPermission.typing {
			return fmt.Errorf("cancelTyping without typing")
		}
		m.pendingPermission.typing = false
		m.input.SetValue(m.pendingPermission.savedDraft)
		m.pendingPermission.savedDraft = ""
	case PromptEvResolvePermission:
		pending := m.pendingPermission
		if pending == nil {
			return fmt.Errorf("resolvePermission without prompt")
		}
		if pending.decide != nil {
			pending.decide(agent.PermissionDecision{Action: agent.PermissionDecisionAction(a.decision), Reason: a.reason})
		}
		// A feedback submit restores the composer draft; a plain resolve
		// leaves the composer alone. Typing twins are dropped with the struct.
		if pending.typing {
			m.input.SetValue(pending.savedDraft)
		}
		m.pendingPermission = nil
		m.lastStreamActivity = m.now()
	case PromptEvResolveAskUser:
		pending := m.pendingAskUser
		if pending == nil {
			return fmt.Errorf("resolveAskUser without prompt")
		}
		if pending.answer != nil {
			pending.answer(a.answers)
		}
		m.pendingAskUser = nil
		m.clearComposer()
		m.clearSuggestions()
	case PromptEvResolveSpecReview:
		m.pendingSpecReview = nil
	case PromptEvCancelAll:
		// cancelRun semantics: restore a typing draft, drop tool/ask prompts,
		// keep spec review and peer approval alive (idle modals, not part of
		// the run). Peer keeps both its queue entry and its prompt so the
		// card never orphans its approval.
		if m.peerPendingApproval != nil {
			return nil
		}
		if m.pendingPermission != nil && m.pendingPermission.typing {
			m.input.SetValue(m.pendingPermission.savedDraft)
		}
		m.pendingPermission = nil
		// An ask_user answer must fire exactly once: the run cancel abandons
		// the questionnaire, so answer with nil (mirrors agentResponseMsg).
		// The agent side also unblocks via context; the buffered channel
		// keeps this non-blocking and idempotent.
		if m.pendingAskUser != nil {
			if m.pendingAskUser.answer != nil {
				m.pendingAskUser.answer(nil)
			}
			m.pendingAskUser = nil
		}
	case PromptEvExpirePeer:
		id := a.expireID
		if m.peerPendingApproval != nil && m.peerPendingApproval.ID == id {
			m.peerPendingApproval = nil
			if m.pendingPermission != nil && m.pendingPermission.request.ToolCallID == "peer-"+id {
				m.pendingPermission = nil
			}
			return nil
		}
		for i, msg := range m.peerApprovalQueue {
			if msg.ID == id {
				m.peerApprovalQueue = append(m.peerApprovalQueue[:i], m.peerApprovalQueue[i+1:]...)
				break
			}
		}
	case PromptEvReleasePeer:
		if a.released == nil {
			return fmt.Errorf("releasePeer without message")
		}
		id := a.released.ID
		if m.peerPendingApproval != nil && m.peerPendingApproval.ID == id {
			m.peerPendingApproval = nil
			if m.pendingPermission != nil && m.pendingPermission.request.ToolCallID == "peer-"+id {
				m.pendingPermission = nil
			}
		}
		for i, q := range m.peerApprovalQueue {
			if q.ID == id {
				m.peerApprovalQueue = append(m.peerApprovalQueue[:i], m.peerApprovalQueue[i+1:]...)
				break
			}
		}
	}
	return nil
}

// pendingActionToAgent maps the TUI decision to the agent action. The TUI
// permissionDecision is an alias of the agent action, so this is identity.
