package tui

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// slAtomicSwap exchanges two paths in one step (renamex_np RENAME_SWAP on
// macOS, renameat2 RENAME_EXCHANGE on Linux). It stays nil where the OS has
// no such call.
var slAtomicSwap func(a, b string) error

// slRequireAtomicSwap skips unless this OS and filesystem can exchange a
// directory and a symlink atomically.
func slRequireAtomicSwap(t *testing.T) func(a, b string) error {
	t.Helper()
	if slAtomicSwap == nil {
		t.Skipf("no atomic path exchange on %s", runtime.GOOS)
	}
	dir := t.TempDir()
	dirPath, linkPath := filepath.Join(dir, "dir"), filepath.Join(dir, "link")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, linkPath); err != nil {
		t.Fatal(err)
	}
	if err := slAtomicSwap(dirPath, linkPath); err != nil {
		t.Skipf("atomic path exchange unsupported here: %v", err)
	}
	return slAtomicSwap
}

// slSwapLoop keeps exchanging the staging dir with a link to outside until
// stop closes, then leaves the real dir back in place. When QG removes the
// planted link, the loop puts the real dir back and plants a new link.
func slSwapLoop(swap func(a, b string) error, staging, outside string, stop <-chan struct{}) *sync.WaitGroup {
	parked := staging + ".swap"
	_ = os.Symlink(outside, parked)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				if info, err := os.Lstat(staging); err != nil {
					_ = os.Rename(parked, staging)
				} else if info.Mode()&os.ModeSymlink != 0 {
					_ = swap(staging, parked)
				}
				if info, err := os.Lstat(parked); err == nil && info.Mode()&os.ModeSymlink != 0 {
					_ = os.Remove(parked)
				}
				return
			default:
			}
			if swap(staging, parked) != nil {
				continue
			}
			if swap(staging, parked) != nil {
				_ = os.Rename(parked, staging)
				_ = os.Symlink(outside, parked)
			}
		}
	}()
	return &wg
}

// slRegularFiles lists every regular file under dir, relative and sorted.
// Directories (an emptied staging or dry-run dir) do not count.
func slRegularFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(files)
	return files
}

// slSetupSettled reports a setup turn that ended in a preview or a refusal.
func slSetupSettled(frame string) bool {
	plain := plainFrame(frame)
	for _, s := range []string{"Prévia com os dados desta sessão", "proposta recusada", "rejeitado (", "não gerou proposta", "staging inválido", "staging ilegível"} {
		if strings.Contains(plain, s) {
			return true
		}
	}
	return false
}

// Discard checks the staging dir and removes only through that
// checked directory. A loop exchanging the dir with a link to an outside
// dir between the check and the removal must never delete outside files.
func TestStatusLineDiscardRacingStagingSwapKeepsOutsideFiles(t *testing.T) {
	swap := slRequireAtomicSwap(t)
	root := t.TempDir()
	staging := filepath.Join(root, "qg", "statusline", "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := os.Lstat(staging)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "fora")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "proposal.json")
	if err := os.WriteFile(victim, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagingRoot, err := os.OpenRoot(staging)
	if err != nil {
		t.Fatal(err)
	}
	defer stagingRoot.Close()
	stop := make(chan struct{})
	wg := slSwapLoop(swap, staging, outside, stop)
	calls, removed := 0, 0
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := stagingRoot.WriteFile("proposal.json", []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		discardStatuslineStaging(staging, guard, "", "")
		calls++
		if _, err := stagingRoot.Lstat("proposal.json"); err != nil {
			removed++
		}
		if _, err := os.Lstat(victim); err != nil {
			break
		}
	}
	close(stop)
	wg.Wait()
	if _, err := os.Lstat(victim); err != nil {
		t.Fatalf("discard deleted %s outside the staging dir (call %d): %v", victim, calls, err)
	}
	if removed == 0 {
		t.Fatalf("discard never removed the staging proposal in %d calls", calls)
	}
}

// Finalize reads the proposal and the script only through the checked
// staging dir. While a loop exchanges the dir with a link to an outside dir,
// outside content never reaches the dry run.
func TestStatusLineAcceptance_FinalizeReadsOnlyVerifiedStaging(t *testing.T) {
	requireSh(t)
	swap := slRequireAtomicSwap(t)
	for i := 0; i < 20; i++ {
		ok := t.Run(fmt.Sprintf("round%02d", i), func(t *testing.T) {
			env := newSLEnv(t)
			writeJSONFile(t, env.userConfig, slUserConfigBase())
			marker := filepath.Join(env.root, "outside-ran")
			outside := filepath.Join(env.root, "fora")
			stop := make(chan struct{})
			var loop *sync.WaitGroup
			provider, _ := slStageOnce(env, func(staging string) bool {
				if !slStageScript(staging, "#!/bin/sh\nprintf 'SL-OK\\n'\n") {
					return false
				}
				if os.MkdirAll(outside, 0o700) != nil || !slStageScript(outside, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'SL-OUTSIDE\\n'\n") {
					return false
				}
				loop = slSwapLoop(swap, staging, outside, stop)
				return true
			})
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			p.submit("/statusline quero uma linha minha")
			frame, settled := p.waitFor(8*time.Second, slSetupSettled)
			close(stop)
			if loop != nil {
				loop.Wait()
			}
			if !settled {
				t.Fatalf("the setup turn never settled:\n%s", plainFrame(frame))
			}
			if strings.Contains(plainFrame(frame), "Prévia com os dados desta sessão") {
				p.key(tea.KeyEscape)
				p.runFor(300 * time.Millisecond)
			}
			if _, err := os.Stat(marker); err == nil || strings.Contains(plainFrame(frame), "SL-OUTSIDE") {
				t.Errorf("the dry run ran a script from a directory swapped in after the staging check")
			}
		})
		if !ok {
			break
		}
	}
}

// An approved script survives the next setup turn whatever its name:
// QG sweeps only its own dry-run area, never the installed scripts.
func TestStatusLineAcceptance_InstalledScriptSurvivesNextSetup(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	var mu sync.Mutex
	turn := 0
	provider := &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
			return
		}
		turn++
		name, out := "dryrun-live.sh", "SL-LIVE"
		if turn > 1 {
			name, out = "b.sh", "SL-B"
		}
		if os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte("#!/bin/sh\nprintf '"+out+"\\n'\n"), 0o600) != nil {
			return
		}
		_ = os.WriteFile(path, []byte(`{"lines":[{"left":["mode"]}],"script":"`+name+`","refreshMs":1000,"timeoutMs":8000}`), 0o600)
	}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline primeira")
	if frame, ok := p.waitFor(8*time.Second, containsPlain("SL-LIVE")); !ok {
		t.Fatalf("no first preview:\n%s", plainFrame(frame))
	}
	p.key(tea.KeyEnter)
	if frame, ok := p.waitFor(5*time.Second, func(f string) bool { return strings.Contains(slFooter(plainFrame(f)), "SL-LIVE") }); !ok {
		t.Fatalf("the approved script never reached the footer:\n%s", slFooter(plainFrame(frame)))
	}
	installed := filepath.Join(env.scriptsDir, "dryrun-live.sh")
	want, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("no installed script: %v", err)
	}
	p.submit("/statusline segunda")
	if frame, ok := p.waitFor(8*time.Second, containsPlain("SL-B")); !ok {
		t.Fatalf("no second preview:\n%s", plainFrame(frame))
	}
	if got, err := os.ReadFile(installed); err != nil || !bytes.Equal(got, want) {
		t.Errorf("the next setup turn swept the approved script %s: %v", installed, err)
	}
	p.key(tea.KeyEscape)
	p.runFor(500 * time.Millisecond)
	restart := startSLPump(t, newSLModel(t, env, nil), 120, 40)
	if frame, ok := restart.waitFor(4*time.Second, func(f string) bool { return strings.Contains(slFooter(plainFrame(f)), "SL-LIVE") }); !ok {
		t.Errorf("after a cancelled setup and a restart the footer lost the approved script:\n%s", slFooter(plainFrame(frame)))
	}
}

// With the staging dir swapped for a link after the preview, Esc and
// Enter still remove the dry-run copy QG keeps outside the staging dir.
func TestStatusLineAcceptance_SwapAfterPreviewLeavesNoCopy(t *testing.T) {
	requireSh(t)
	for _, key := range []string{"esc", "enter"} {
		t.Run(key, func(t *testing.T) {
			env := newSLEnv(t)
			writeJSONFile(t, env.userConfig, slUserConfigBase())
			provider, staged := slStageOnce(env, func(staging string) bool {
				return slStageScript(staging, "#!/bin/sh\nprintf 'SL-OK\\n'\n")
			})
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			p.submit("/statusline quero uma linha minha")
			if frame, ok := p.waitFor(8*time.Second, containsPlain("Prévia com os dados desta sessão")); !ok {
				t.Fatalf("no preview:\n%s", plainFrame(frame))
			}
			outside := filepath.Join(env.root, "fora")
			if err := os.MkdirAll(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			staging := staged()
			if err := os.Rename(staging, filepath.Join(env.root, "staging.moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, staging); err != nil {
				t.Fatal(err)
			}
			want := ""
			if key == "esc" {
				p.key(tea.KeyEscape)
			} else {
				p.key(tea.KeyEnter)
				want = "x.sh"
			}
			p.runFor(1500 * time.Millisecond)
			if got := strings.Join(slRegularFiles(t, env.scriptsDir), ","); got != want {
				t.Errorf("after %s on a swapped staging the scripts dir keeps files %q, want %q", key, got, want)
			}
		})
	}
}

// A fields-only proposal discards through the same identity check:
// with the staging dir replaced by another real dir after the preview, Esc
// and Enter leave that dir's files alone.
func TestStatusLineAcceptance_FieldsOnlyDiscardChecksStagingIdentity(t *testing.T) {
	for _, key := range []string{"esc", "enter"} {
		t.Run(key, func(t *testing.T) {
			env := newSLEnv(t)
			writeJSONFile(t, env.userConfig, slUserConfigBase())
			provider, staged := slStageOnce(env, func(staging string) bool {
				return os.WriteFile(filepath.Join(staging, "proposal.json"), []byte(`{"lines":[{"left":["mode","branch"]}]}`), 0o600) == nil
			})
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			p.submit("/statusline põe o branch do lado do modo")
			if frame, ok := p.waitFor(8*time.Second, containsPlain("Prévia com os dados desta sessão")); !ok {
				t.Fatalf("no preview:\n%s", plainFrame(frame))
			}
			other := filepath.Join(env.root, "other")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, "proposal.json"), []byte("OTHER"), 0o600); err != nil {
				t.Fatal(err)
			}
			staging := staged()
			if err := os.Rename(staging, filepath.Join(env.root, "staging.moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, staging); err != nil {
				t.Fatal(err)
			}
			if key == "esc" {
				p.key(tea.KeyEscape)
			} else {
				p.key(tea.KeyEnter)
			}
			p.runFor(time.Second)
			if data, err := os.ReadFile(filepath.Join(staging, "proposal.json")); err != nil || string(data) != "OTHER" {
				t.Errorf("%s deleted or changed proposal.json in a directory swapped in after the preview: %v", key, err)
			}
		})
	}
}
