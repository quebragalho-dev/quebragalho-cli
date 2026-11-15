package mcp

import "fmt"

// MCP server membership as an explicit state machine.
//
//	stateDiagram-v2
//	[*] --> disabled: configured with disabled=true (never connected)
//	[*] --> skipped: connect/list/validate failed at startup
//	[*] --> live: connected and tools adopted
//	[*] --> needs_auth: OAuth server without a usable token
//	skipped --> live: Reconnect ok / background adopt
//	needs_auth --> needs_auth: Reconnect ok (tools refresh, token still missing)
//	live --> live: Reconnect ok (re-adopt) / relist ok
//	skipped --> skipped: background failure refreshes the reason
//	disabled --> *: refused, never dials (enable with qg mcp enable first)
//	* --> closed: Runtime.Close latches the runtime
//
// Status is a projection of this enum (membershipLocked); adoptRelistLocked
// is the single writer of the live side and replaceSkippedLocked the single
// writer of the skipped side. Closed stays what it is in the code: the
// runtime-wide closed latch, consulted directly by Reconnect and Close, so
// Status rows stay byte-identical before and after Close.
type memberState int

const (
	memberDisabled memberState = iota
	memberSkipped
	memberNeedsAuth
	memberLive
	memberClosed
)

// String renders the membership state with its stable /mcp name.
func (s memberState) String() string {
	switch s {
	case memberDisabled:
		return "disabled"
	case memberSkipped:
		return "skipped"
	case memberNeedsAuth:
		return "needs_auth"
	case memberLive:
		return "live"
	case memberClosed:
		return "closed"
	default:
		return "unknown"
	}
}

type memberEvent int

const (
	memberEventReconnect memberEvent = iota
	memberEventAdopt
	memberEventFail
	memberEventClose
)

// String renders the membership event for tables and errors.
func (e memberEvent) String() string {
	switch e {
	case memberEventReconnect:
		return "reconnect"
	case memberEventAdopt:
		return "adopt"
	case memberEventFail:
		return "fail"
	case memberEventClose:
		return "close"
	default:
		return "unknown"
	}
}

// legalMemberTransition is the single table of legal membership transitions.
// It returns the projected membership after the event; ok=false refuses and
// the caller must neither dial nor mutate. Reconnect consults it before
// dialing, so a disabled server never gains a process.
func legalMemberTransition(from memberState, event memberEvent) (to memberState, ok bool) {
	switch from {
	case memberDisabled:
		return from, false
	case memberSkipped:
		switch event {
		case memberEventReconnect, memberEventAdopt:
			return memberLive, true
		case memberEventFail:
			return memberSkipped, true
		case memberEventClose:
			return memberClosed, true
		}
		return from, false
	case memberNeedsAuth:
		switch event {
		case memberEventReconnect, memberEventAdopt, memberEventFail:
			return memberNeedsAuth, true
		case memberEventClose:
			return memberClosed, true
		}
		return from, false
	case memberLive:
		switch event {
		case memberEventReconnect, memberEventAdopt:
			return memberLive, true
		case memberEventClose:
			return memberClosed, true
		}
		return from, false
	case memberClosed:
		return from, false
	}
	return from, false
}

// membershipLocked reports the projected membership of one server. Callers
// hold refreshMu. The precedence (disabled > skipped > needs-auth > live)
// mirrors the historical Status branches, so Status output is unchanged.
func (runtime *Runtime) membershipLocked(name string) memberState {
	if runtime.disabled[name] {
		return memberDisabled
	}
	for _, skipped := range runtime.skipped {
		if skipped.Name == name {
			return memberSkipped
		}
	}
	for _, server := range runtime.servers {
		if server.Name != name {
			continue
		}
		if serverRequiresOAuth(server) && !hasUsableOAuthToken(server) {
			return memberNeedsAuth
		}
		return memberLive
	}
	return memberLive
}

// disabledServerError is the actionable refusal for reconnecting a disabled
// server, in the style of the neighboring Portuguese errors (MissingEnvError).
func disabledServerError(serverName string) error {
	return fmt.Errorf("Servidor MCP %s está desabilitado (habilite com `qg mcp enable %s`)", serverName, serverName)
}
