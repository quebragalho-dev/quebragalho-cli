package tui

import (
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

func TestParseLogoutCommand(t *testing.T) {
	if got := parseCommand("/logout"); got.kind != commandLogout {
		t.Fatalf("/logout kind = %v, want %v", got.kind, commandLogout)
	}
}

func TestLogoutAppearsInHelp(t *testing.T) {
	if help := strings.Join(formatCommandHelpLines(), "\n"); !strings.Contains(help, "/logout") {
		t.Fatalf("help missing /logout:\n%s", help)
	}
}

func TestLogoutCommandQuitsAfterMessage(t *testing.T) {
	m := newLogoutTestModel(t, func() (LogoutSummary, error) {
		return LogoutSummary{Message: "Sessao encerrada."}, nil
	})
	m.input.SetValue("/logout")
	next, cmd := m.Update(testKey(tea.KeyEnter))
	if !isQuit(cmd) {
		t.Fatal("/logout must quit the app after a successful logout")
	}
	if !transcriptContains(next.(model).transcript, "Sessao encerrada.") {
		t.Fatalf("summary message not shown: %#v", next.(model).transcript)
	}
}

func TestLogoutCommandErrorKeepsAppOpen(t *testing.T) {
	m := newLogoutTestModel(t, func() (LogoutSummary, error) {
		return LogoutSummary{}, errors.New("keychain locked")
	})
	m.input.SetValue("/logout")
	next, cmd := m.Update(testKey(tea.KeyEnter))
	if isQuit(cmd) {
		t.Fatal("a failed logout must not close the app")
	}
	if !transcriptContains(next.(model).transcript, "keychain locked") {
		t.Fatalf("error not shown: %#v", next.(model).transcript)
	}
}

func TestLogoutCommandUnavailableKeepsAppOpen(t *testing.T) {
	m := newLogoutTestModel(t, nil)
	m.input.SetValue("/logout")
	next, cmd := m.Update(testKey(tea.KeyEnter))
	if isQuit(cmd) {
		t.Fatal("/logout without a handler must not close the app")
	}
	if !transcriptContains(next.(model).transcript, "indisponível") {
		t.Fatalf("unavailable notice not shown: %#v", next.(model).transcript)
	}
}
