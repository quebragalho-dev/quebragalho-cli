package mcp

import (
	"errors"
	"strings"
	"testing"
)

// Commit B (M2): the membership state x event table, fixed literally. The
// implementation consults legalMemberTransition; this test pins every
// combination so a stray row (e.g. allowing disabled -> live) fails loudly.
func TestLegalMemberTransitionCoversAllStateEventPairs(t *testing.T) {
	states := []memberState{memberDisabled, memberSkipped, memberNeedsAuth, memberLive, memberClosed}
	events := []memberEvent{memberEventReconnect, memberEventAdopt, memberEventFail, memberEventClose}
	type outcome struct {
		to memberState
		ok bool
	}
	want := map[memberState]map[memberEvent]outcome{
		memberDisabled: {
			memberEventReconnect: {memberDisabled, false},
			memberEventAdopt:     {memberDisabled, false},
			memberEventFail:      {memberDisabled, false},
			memberEventClose:     {memberDisabled, false},
		},
		memberSkipped: {
			memberEventReconnect: {memberLive, true},
			memberEventAdopt:     {memberLive, true},
			memberEventFail:      {memberSkipped, true},
			memberEventClose:     {memberClosed, true},
		},
		memberNeedsAuth: {
			memberEventReconnect: {memberNeedsAuth, true},
			memberEventAdopt:     {memberNeedsAuth, true},
			memberEventFail:      {memberNeedsAuth, true},
			memberEventClose:     {memberClosed, true},
		},
		memberLive: {
			memberEventReconnect: {memberLive, true},
			memberEventAdopt:     {memberLive, true},
			memberEventFail:      {memberLive, false},
			memberEventClose:     {memberClosed, true},
		},
		memberClosed: {
			memberEventReconnect: {memberClosed, false},
			memberEventAdopt:     {memberClosed, false},
			memberEventFail:      {memberClosed, false},
			memberEventClose:     {memberClosed, false},
		},
	}
	for _, from := range states {
		for _, event := range events {
			gotTo, gotOK := legalMemberTransition(from, event)
			wantOutcome := want[from][event]
			if gotTo != wantOutcome.to || gotOK != wantOutcome.ok {
				t.Errorf("legalMemberTransition(%s, %s) = (%s, %v), want (%s, %v)",
					from, event, gotTo, gotOK, wantOutcome.to, wantOutcome.ok)
			}
		}
	}
	for _, state := range states {
		if strings.TrimSpace(state.String()) == "" || state.String() == "unknown" {
			t.Errorf("memberState.String() is empty for %d", int(state))
		}
	}
	for _, event := range events {
		if strings.TrimSpace(event.String()) == "" || event.String() == "unknown" {
			t.Errorf("memberEvent.String() is empty for %d", int(event))
		}
	}
}

// membershipLocked is the single projection behind Status: disabled wins,
// then skipped, then needs-auth, then live.
func TestMembershipLockedProjectsEveryState(t *testing.T) {
	acceptanceHermeticEnv(t)
	runtime := &Runtime{
		servers: []Server{
			{Name: "ok"},
			{Name: "bad"},
			{Name: "login", Auth: ServerAuthOAuth},
			{Name: "off"},
		},
		disabled:    map[string]bool{"off": true},
		skipped:     []SkippedServer{{Name: "bad", Err: errors.New("boom")}},
		liveClients: map[string]*mcpServerConn{"ok": {}, "login": {}},
		serverTools: map[string][]string{"ok": {"mcp_ok_lookup"}},
	}
	cases := map[string]memberState{
		"off":   memberDisabled,
		"bad":   memberSkipped,
		"login": memberNeedsAuth,
		"ok":    memberLive,
	}
	for name, want := range cases {
		if got := runtime.membershipLocked(name); got != want {
			t.Errorf("membershipLocked(%q) = %s, want %s", name, got, want)
		}
	}
}
