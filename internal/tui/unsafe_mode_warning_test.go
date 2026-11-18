package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func TestUnsafeModeWarningAppearsAtStartup(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd:            t.TempDir(),
		PermissionMode: agent.PermissionModeUnsafe,
	})
	text := transcriptText(m.transcript)
	for _, want := range []string{"sem isolamento", "acesso total ao computador"} {
		if !strings.Contains(text, want) {
			t.Fatalf("unsafe startup transcript missing %q: %q", want, text)
		}
	}

	for _, safeMode := range []agent.PermissionMode{agent.PermissionModeAsk, agent.PermissionModeAuto, agent.PermissionModePlan} {
		safeModel := newModel(context.Background(), Options{
			Cwd:            t.TempDir(),
			PermissionMode: safeMode,
		})
		if transcriptContains(safeModel.transcript, "acesso total ao computador") {
			t.Fatalf("mode %q should not emit unsafe mode warning: %q", safeMode, transcriptText(safeModel.transcript))
		}
	}
}
