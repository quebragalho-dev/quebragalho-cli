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
	PromptEvReplacePermission
	PromptEvReplaceAskUser
	PromptEvEnterTyping
	PromptEvCancelTyping
	PromptEvResolvePermission
	PromptEvResolveAskUser
	PromptEvResolveSpecReview
	PromptEvCancelRun
	PromptEvSettleEnd
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
	case PromptEvReplacePermission:
		return "replacePermission"
	case PromptEvReplaceAskUser:
		return "replaceAskUser"
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
	case PromptEvCancelRun:
		return "cancelRun"
	case PromptEvSettleEnd:
		return "settleEnd"
	case PromptEvExpirePeer:
		return "expirePeer"
	case PromptEvReleasePeer:
		return "releasePeer"
	default:
		return "unknown"
	}
}

// promptLegal is the single table of legal (phase x event) transitions.
// Replace/expire/release/cancelRun/settleEnd are legal in every busy phase to
// preserve the exact pre-machine effects: a new tool/ask request overwrote the
// live prompt (leaking the old callback — open item, not fixed here), and a
// queued peer expire/release removed its queue entry even with a modal open.
var promptLegal = map[PromptPhase]map[PromptEvent]bool{
	PromptNone: {
		PromptEvOpenPermission: true,
		PromptEvOpenAskUser:    true,
		PromptEvOpenSpecReview: true,
		PromptEvOpenPeer:       true,
		PromptEvCancelRun:      true,
		PromptEvSettleEnd:      true,
		PromptEvExpirePeer:     true,
		PromptEvReleasePeer:    true,
	},
	PromptPermission: {
		PromptEvReplacePermission: true,
		PromptEvReplaceAskUser:    true,
		PromptEvEnterTyping:       true,
		PromptEvResolvePermission: true,
		PromptEvCancelRun:         true,
		PromptEvSettleEnd:         true,
		PromptEvExpirePeer:        true,
		PromptEvReleasePeer:       true,
	},
	PromptPermissionTyping: {
		PromptEvReplacePermission: true,
		PromptEvReplaceAskUser:    true,
		PromptEvCancelTyping:      true,
		PromptEvResolvePermission: true,
		PromptEvCancelRun:         true,
		PromptEvSettleEnd:         true,
		PromptEvExpirePeer:        true,
		PromptEvReleasePeer:       true,
	},
	PromptAskUser: {
		PromptEvReplacePermission: true,
		PromptEvReplaceAskUser:    true,
		PromptEvResolveAskUser:    true,
		PromptEvCancelRun:         true,
		PromptEvSettleEnd:         true,
		PromptEvExpirePeer:        true,
		PromptEvReleasePeer:       true,
	},
	PromptSpecReview: {
		PromptEvReplacePermission: true,
		PromptEvReplaceAskUser:    true,
		PromptEvResolveSpecReview: true,
		PromptEvCancelRun:         true,
		PromptEvSettleEnd:         true,
		PromptEvExpirePeer:        true,
		PromptEvReleasePeer:       true,
	},
	PromptPeerApproval: {
		PromptEvReplacePermission: true,
		PromptEvReplaceAskUser:    true,
		PromptEvResolvePermission: true,
		PromptEvCancelRun:         true,
		PromptEvSettleEnd:         true,
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
// with an error while keeping state, and fixes twin fields together.
// Replace events restore the exact pre-machine overwrite (new request drops
// the live prompt without resolving the old callback — open item, not fixed).
// CancelRun restores a typing draft (proven fix) and drops tool/ask prompts
// without answering (pre-machine parity); SettleEnd answers ask with nil
// (agentResponseMsg parity) without restoring a draft. A peer expire/release
// only clears a pending prompt whose ToolCallID matches; queue entries are
// removed in every phase (pre-machine parity). Opening (or replacing) any
// prompt cancels a pending leader chord: the follow-up key must go to the
// modal, never to a stale Ctrl+X slash (S2 fix; the chord's tick is
// invalidated via the seq bump, like clearLeader).
func (m *model) promptTransition(ev PromptEvent, a promptArgs) error {
	phase := m.promptPhase()
	if !promptEventLegal(phase, ev) {
		return fmt.Errorf("illegal prompt transition %s in %s", ev, phase)
	}
	switch ev {
	case PromptEvOpenPermission, PromptEvOpenAskUser, PromptEvOpenSpecReview, PromptEvOpenPeer, PromptEvReplacePermission, PromptEvReplaceAskUser:
		if m.leaderPending {
			m.leaderPending = false
			m.leaderSeq++
		}
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
	case PromptEvReplacePermission:
		// Pre-machine parity: a new tool request overwrote the live prompt
		// without resolving the old decide (open item, not fixed here).
		// Other prompt fields are left untouched (split-brain preserved).
		if a.permReq == nil {
			return fmt.Errorf("replacePermission without request")
		}
		m.pendingPermission = &pendingPermissionPrompt{request: *a.permReq, decide: a.permDecide}
	case PromptEvReplaceAskUser:
		// Pre-machine parity: a new ask_user request overwrote pendingAskUser
		// without answering the old one (open item, not fixed here).
		if a.askReq == nil {
			return fmt.Errorf("replaceAskUser without request")
		}
		states := a.askStates
		if states == nil {
			states = newAskUserStates(a.askReq.Questions)
		}
		m.pendingAskUser = &pendingAskUserPrompt{request: *a.askReq, answer: a.askAnswer, states: states}
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
	case PromptEvCancelRun:
		// Pre-machine cancelRun parity plus the proven draft fix: restore a
		// typing draft, drop the tool prompt (even a peer card, leaving its
		// approval orphaned as before) and drop ask WITHOUT answering.
		// Spec review is preserved (idle modal, proven intentional).
		if m.pendingPermission != nil && m.pendingPermission.typing {
			m.input.SetValue(m.pendingPermission.savedDraft)
		}
		m.pendingPermission = nil
		m.pendingAskUser = nil
	case PromptEvSettleEnd:
		// Pre-machine agentResponseMsg parity: drop the tool prompt without
		// restoring a draft, answer a live ask with nil, preserve spec/peer.
		m.pendingPermission = nil
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
