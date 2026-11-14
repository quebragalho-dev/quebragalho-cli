package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// statusline/.dryrun is reserved and private. A link planted there
// must never lead the stale-copy sweep or the fresh copy outside it: the
// setup refuses or replaces the link, and outside files stay as they were.
func TestStatusLineAcceptance_DryRunDirLinkKeepsOutsideFiles(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	outside := filepath.Join(env.root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "dryrun-keep.sh"), []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.scriptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(env.scriptsDir, ".dryrun")); err != nil {
		t.Fatal(err)
	}
	provider, _ := slStageOnce(env, func(staging string) bool {
		return slStageScript(staging, "#!/bin/sh\nprintf 'SL-OK\\n'\n")
	})
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	settled := func(frame string) bool {
		return slSetupSettled(frame) || strings.Contains(plainFrame(frame), "não foi possível preparar")
	}
	if frame, ok := p.waitFor(8*time.Second, settled); !ok {
		t.Fatalf("setup never settled:\n%s", plainFrame(frame))
	}
	if got := strings.Join(slRegularFiles(t, outside), ","); got != "dryrun-keep.sh" {
		t.Errorf("dry run went through the planted .dryrun link: outside dir holds %q", got)
	}
	p.key(tea.KeyEscape)
	p.runFor(time.Second)
	if got := strings.Join(slRegularFiles(t, outside), ","); got != "dryrun-keep.sh" {
		t.Errorf("after Esc the outside dir holds %q", got)
	}
}
