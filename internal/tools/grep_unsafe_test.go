package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// Unsafe runs do not prompt for out-of-workspace reads, so grep must not
// silently skip them either: the walk is anchored at the requested path and
// DenyRead exclusions keep applying per file.
func TestGrepUnsafeFindsOutsideWorkspaceMatches(t *testing.T) {
	root := t.TempDir()
	outside := tempDirOutsideDefaultTemp(t)
	target := filepath.Join(outside, "notes.txt")
	if err := os.WriteFile(target, []byte("needle in the haystack\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewScopedGrepTool(root, nil).(interface {
		RunWithOptions(context.Context, map[string]any, RunOptions) Result
	})
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    outside,
	}, RunOptions{PermissionMode: string(sandbox.PermissionUnsafe)})

	if result.Status != StatusOK {
		t.Fatalf("unsafe outside grep = %s, want ok: %s", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "needle") {
		t.Fatalf("unsafe outside grep missed the match, got: %q", result.Output)
	}
}

// Outside the unsafe mode the boundary still holds.
func TestGrepOutsideWorkspaceStaysDeniedWithoutUnsafe(t *testing.T) {
	root := t.TempDir()
	outside := tempDirOutsideDefaultTemp(t)
	target := filepath.Join(outside, "notes.txt")
	if err := os.WriteFile(target, []byte("needle in the haystack\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewScopedGrepTool(root, nil).Run(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    outside,
	})
	if result.Status != StatusError || !strings.Contains(result.Output, "must stay inside the workspace") {
		t.Fatalf("non-unsafe outside grep = %s, want the workspace-boundary error: %q", result.Status, result.Output)
	}
}

// Even in unsafe mode a DenyRead path must not surface.
func TestGrepUnsafeStillHonorsDenyRead(t *testing.T) {
	root := t.TempDir()
	outside := tempDirOutsideDefaultTemp(t)
	allowed := filepath.Join(outside, "allowed.txt")
	secretDir := filepath.Join(outside, "secret")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(secretDir, "creds.txt")
	if err := os.WriteFile(allowed, []byte("needle in the open\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(denied, []byte("needle in the secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := sandbox.NewEngine(sandbox.EngineOptions{
		WorkspaceRoot: root,
		Policy: sandbox.Policy{
			Mode:             sandbox.ModeEnforce,
			EnforceWorkspace: true,
			DenyRead:         []string{secretDir},
		},
	})

	tool := NewScopedGrepTool(root, nil).(interface {
		RunWithOptions(context.Context, map[string]any, RunOptions) Result
	})
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    outside,
	}, RunOptions{PermissionMode: string(sandbox.PermissionUnsafe), Sandbox: engine})

	if result.Status != StatusOK {
		t.Fatalf("unsafe grep status = %s, want ok: %s", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "allowed.txt") {
		t.Fatalf("unsafe grep missed the allowed match, got: %q", result.Output)
	}
	if strings.Contains(result.Output, "creds.txt") || strings.Contains(result.Output, "needle in the secret") {
		t.Fatalf("unsafe grep surfaced DenyRead content, got: %q", result.Output)
	}
}
