package cli

import (
	"bytes"
	"context"
	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
	"strings"
	"testing"
)

func TestBatchSevenPermissionFlagNames(t *testing.T) {
	for _, flag := range []string{"--dangerously-skip-permissions", "--skip-permissions-unsafe"} {
		t.Run(flag, func(t *testing.T) {
			opts, _, err := parseExecArgs([]string{flag, "-p", "test"})
			if err != nil || !opts.skipPermissionsUnsafe {
				t.Fatalf("parse %s: %+v %v", flag, opts, err)
			}
			_, _, err = parseExecArgs([]string{flag, "--plan", "-p", "test"})
			if err == nil || !strings.Contains(err.Error(), "--dangerously-skip-permissions") {
				t.Fatalf("plan conflict: %v", err)
			}
			var stdout, stderr bytes.Buffer
			launched := false
			code := runWithDeps([]string{flag}, &stdout, &stderr, appDeps{
				getwd:         func() (string, error) { return t.TempDir(), nil },
				resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) { return usableResolvedConfig(3), nil },
				runTUI: func(_ context.Context, o tui.Options) int {
					launched = true
					if o.PermissionMode != agent.PermissionModeUnsafe {
						t.Errorf("mode=%s", o.PermissionMode)
					}
					return 0
				},
			})
			if code != 0 || !launched {
				t.Fatalf("launch %s code=%d %s", flag, code, stderr.String())
			}
		})
	}
}
