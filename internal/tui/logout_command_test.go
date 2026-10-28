package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newLogoutTestModel(t *testing.T, logout func() (LogoutSummary, error)) model {
	t.Helper()
	return newModel(context.Background(), Options{Cwd: t.TempDir(), Logout: logout})
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestLogoutAppearsInHelp(t *testing.T) {
	if help := strings.Join(formatCommandHelpLines(), "\n"); !strings.Contains(help, "/logout") {
		t.Fatalf("help missing /logout:\n%s", help)
	}
}

// The TUI runs in the alt screen, so the in-app transcript vanishes on quit:
// the confirmation must be written to the real output after the program exits.
func TestLogoutPrintsConfirmationAfterProgramExits(t *testing.T) {
	m := newLogoutTestModel(t, func() (LogoutSummary, error) {
		return LogoutSummary{Message: "Logged out.\nWarning: QG_API_KEY is still set in your environment and stays active; unset it to fully log out."}, nil
	})
	m.input.SetValue("/logout")
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&bytes.Buffer{}))
	go p.Send(testKey(tea.KeyEnter))
	final, err := p.Run()
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	var out bytes.Buffer
	writeFarewell(&out, final)
	for _, want := range []string{"Logged out.", "QG_API_KEY"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestLogoutCommandErrorKeepsAppOpenAndRedacts(t *testing.T) {
	m := newLogoutTestModel(t, func() (LogoutSummary, error) {
		return LogoutSummary{}, errors.New("keychain locked: Authorization: Bearer sk-abcdef1234567890abcdef1234567890")
	})
	m.input.SetValue("/logout")
	next, cmd := m.Update(testKey(tea.KeyEnter))
	if isQuit(cmd) {
		t.Fatal("a failed logout must not close the app")
	}
	got := next.(model).transcript
	if !transcriptContains(got, "keychain locked") {
		t.Fatalf("error not shown: %#v", got)
	}
	if transcriptContains(got, "sk-abcdef1234567890abcdef1234567890") {
		t.Fatalf("secret leaked into transcript: %#v", got)
	}
}

func TestLogoutCommandUnavailableKeepsAppOpen(t *testing.T) {
	m := newLogoutTestModel(t, nil)
	m.input.SetValue("/logout")
	next, cmd := m.Update(testKey(tea.KeyEnter))
	if isQuit(cmd) {
		t.Fatal("/logout without a handler must not close the app")
	}
	if !transcriptContains(next.(model).transcript, "/logout is not available in this session.") {
		t.Fatalf("unavailable notice not shown: %#v", next.(model).transcript)
	}
}

func TestLogoutRefusedWhileRunPending(t *testing.T) {
	called := false
	m := newLogoutTestModel(t, func() (LogoutSummary, error) {
		called = true
		return LogoutSummary{Message: "x"}, nil
	})
	m.pending = true
	next, cmd := m.dispatchCommand(parseCommand("/logout"))
	if called {
		t.Fatal("credentials must not be erased while a run is pending")
	}
	if isQuit(cmd) {
		t.Fatal("app must stay open")
	}
	if !transcriptContains(next.(model).transcript, "/logout: stop the current run first.") {
		t.Fatalf("refusal notice not shown: %#v", next.(model).transcript)
	}
}
