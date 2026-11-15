package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

// Erros de flags do TUI raiz falam português sem perder a flag literal.
func TestPTBRRootFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		bad  []string
	}{
		{"no-browser fora do TUI", []string{"--no-browser", "version"}, "--no-browser serve só para o TUI interativo", []string{"is only supported for the interactive TUI"}},
		{"theme sem valor", []string{"--theme"}, "--theme exige um valor (auto ou um nome de tema; tente --theme auto)", []string{"requires a value (auto or a theme name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			launched := false
			exitCode := runWithDeps(tc.args, &stdout, &stderr, appDeps{
				getwd:  func() (string, error) { return t.TempDir(), nil },
				runTUI: func(context.Context, tui.Options) int { launched = true; return exitSuccess },
			})
			if exitCode == exitSuccess || launched {
				t.Fatalf("exit = %d launched = %v, want a loud usage error", exitCode, launched)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr missing %q, got %q", tc.want, stderr.String())
			}
			for _, unwanted := range tc.bad {
				if strings.Contains(stderr.String(), unwanted) {
					t.Fatalf("stderr leaked English %q, got %q", unwanted, stderr.String())
				}
			}
		})
	}
}
