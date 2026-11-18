package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// slStageOnce returns a provider that, on the setup turn, lets stage write
// into the staging dir announced by the prompt. It records the staging path.
func slStageOnce(env slEnv, stage func(staging string) bool) (*slProvider, func() string) {
	var mu sync.Mutex
	staged := ""
	provider := &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		if staged != "" {
			return
		}
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" {
			return
		}
		staging := filepath.Dir(path)
		if os.MkdirAll(staging, 0o700) != nil || !stage(staging) {
			return
		}
		staged = staging
	}}
	return provider, func() string {
		mu.Lock()
		defer mu.Unlock()
		return staged
	}
}

func slStageScript(staging, body string) bool {
	if os.WriteFile(filepath.Join(staging, "x.sh"), []byte(body), 0o600) != nil {
		return false
	}
	return os.WriteFile(filepath.Join(staging, "proposal.json"), []byte(`{"lines":[{"left":["mode"]}],"script":"x.sh","refreshMs":1000,"timeoutMs":8000}`), 0o600) == nil
}

func slDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// 38. Enter installs exactly the bytes the preview ran. A staged script
// rewritten after the preview must never be installed or run.
func TestStatusLineAcceptance_InstallUsesPreviewedBytes(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	marker := filepath.Join(env.root, "rewritten-ran")
	previewed := "#!/bin/sh\nprintf 'SL-PREVIEWED\\n'\n"
	provider, staged := slStageOnce(env, func(staging string) bool { return slStageScript(staging, previewed) })
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	if frame, ok := p.waitFor(15*time.Second, containsPlain("SL-PREVIEWED")); !ok {
		t.Fatalf("no preview with the staged output:\n%s", plainFrame(frame))
	}
	rewritten := "#!/bin/sh\ntouch '" + marker + "'\nprintf 'SL-REWRITTEN\\n'\n"
	if err := os.WriteFile(filepath.Join(staged(), "x.sh"), []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	p.key(tea.KeyEnter)
	installedPath := filepath.Join(env.scriptsDir, "x.sh")
	frame, _ := p.waitFor(15*time.Second, func(f string) bool {
		if _, err := os.Stat(installedPath); err != nil {
			return false
		}
		footer := slFooter(plainFrame(f))
		return strings.Contains(footer, "SL-PREVIEWED") || strings.Contains(footer, "SL-REWRITTEN")
	})
	installed, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("no installed script: %v", err)
	}
	if string(installed) != previewed {
		t.Errorf("installed script differs from the previewed one:\n%s", installed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the script rewritten after the preview ran")
	}
	footer := slFooter(plainFrame(frame))
	if !strings.Contains(footer, "SL-PREVIEWED") || strings.Contains(footer, "SL-REWRITTEN") {
		t.Errorf("footer does not show the previewed script's output:\n%s", footer)
	}
}

// 39. After the preview, the staging dir is swapped for a link to an outside
// dir. Neither Esc nor Enter may delete or change anything outside.
func TestStatusLineAcceptance_SwapAfterPreviewKeepsOutsideFiles(t *testing.T) {
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
			if frame, ok := p.waitFor(8*time.Second, containsPlain("Enter aplica")); !ok {
				t.Fatalf("no preview:\n%s", plainFrame(frame))
			}
			outside := filepath.Join(env.root, "fora")
			if err := os.MkdirAll(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"proposal.json", "x.sh"} {
				if err := os.WriteFile(filepath.Join(outside, name), []byte("OUTSIDE"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			staging := staged()
			if err := os.Rename(staging, staging+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, staging); err != nil {
				t.Fatal(err)
			}
			if key == "esc" {
				p.key(tea.KeyEscape)
			} else {
				p.key(tea.KeyEnter)
			}
			p.runFor(1500 * time.Millisecond)
			for _, name := range []string{"proposal.json", "x.sh"} {
				data, err := os.ReadFile(filepath.Join(outside, name))
				if err != nil {
					t.Errorf("QG deleted %s outside the staging dir: %v", name, err)
				} else if string(data) != "OUTSIDE" {
					t.Errorf("QG changed %s outside the staging dir: %q", name, data)
				}
			}
		})
	}
}

// 40. Mid-turn, the staging dir is replaced by a different real directory.
// Nothing from it may run or be deleted, and the config stays untouched.
func TestStatusLineAcceptance_StagingReplacedByAnotherDirRefused(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	marker := filepath.Join(env.root, "other-ran")
	other := filepath.Join(env.root, "other")
	body := "#!/bin/sh\ntouch '" + marker + "'\nprintf 'SL-OTHER\\n'\n"
	provider, _ := slStageOnce(env, func(staging string) bool {
		if !slStageScript(staging, "#!/bin/sh\nprintf 'SL-OK\\n'\n") {
			return false
		}
		if os.MkdirAll(other, 0o700) != nil || !slStageScript(other, body) {
			return false
		}
		return os.Rename(staging, staging+".orig") == nil && os.Rename(other, staging) == nil
	})
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	frame := p.runFor(4 * time.Second)
	if strings.Contains(plainFrame(frame), "Enter aplica") {
		p.key(tea.KeyEscape)
		p.runFor(time.Second)
	}
	if strings.Contains(plainFrame(frame), "SL-OTHER") {
		t.Errorf("the preview ran the replacement directory's script")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("QG ran a script from the replacement directory")
	}
	staging := filepath.Join(env.scriptsDir, "staging")
	for _, name := range []string{"proposal.json", "x.sh"} {
		if data, err := os.ReadFile(filepath.Join(staging, name)); err != nil {
			t.Errorf("QG deleted %s from the replacement directory: %v", name, err)
		} else if name == "x.sh" && string(data) != body {
			t.Errorf("QG changed x.sh in the replacement directory: %q", data)
		}
	}
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Errorf("the user config changed:\n%s", now)
	}
}

// 41. QG never writes into the model's staging area nor changes the staged
// script, and the dry-run copy it keeps elsewhere is gone once the turn ends:
// Esc, Enter, or a failing dry run.
func TestStatusLineAcceptance_DryRunCopyNeverLeftBehind(t *testing.T) {
	requireSh(t)
	cases := []struct {
		name, body, key string
		want            []string
	}{
		{"esc", "#!/bin/sh\nprintf 'SL-OK\\n'\n", "esc", []string{"staging"}},
		{"enter", "#!/bin/sh\nprintf 'SL-OK\\n'\n", "enter", []string{"staging", "x.sh"}},
		{"failing", "#!/bin/sh\nexit 3\n", "", []string{"staging"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSLEnv(t)
			writeJSONFile(t, env.userConfig, slUserConfigBase())
			provider, staged := slStageOnce(env, func(staging string) bool { return slStageScript(staging, tc.body) })
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			p.submit("/statusline quero uma linha minha")
			frame, _ := p.waitFor(8*time.Second, func(f string) bool {
				plain := plainFrame(f)
				return strings.Contains(plain, "Enter aplica") || strings.Contains(plain, "rejeitado")
			})
			if tc.key != "" {
				if !strings.Contains(plainFrame(frame), "Enter aplica") {
					t.Fatalf("no preview:\n%s", plainFrame(frame))
				}
				if got := slDirNames(t, staged()); strings.Join(got, ",") != "proposal.json,x.sh" {
					t.Errorf("staging holds %v at the preview, want only the model's files", got)
				}
				if info, err := os.Stat(filepath.Join(staged(), "x.sh")); err != nil || info.Mode().Perm() != 0o600 {
					t.Errorf("QG changed the staged script at the preview: %v %v", info, err)
				}
				if tc.key == "esc" {
					p.key(tea.KeyEscape)
				} else {
					p.key(tea.KeyEnter)
				}
			}
			p.runFor(1500 * time.Millisecond)
			if got := slDirNames(t, env.scriptsDir); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("scripts dir holds %v after the turn, want %v", got, tc.want)
			}
		})
	}
}

// 42. stdin cache.hit_pct is the same integer the footer shows, also when the
// rate is not an exact float: 29 of 100 input tokens cached reads 29.
func TestStatusLineAcceptance_StdinHitPctIsTheFooterInteger(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	captured := filepath.Join(env.root, "stdin.json")
	command := writeScript(t, env, "cache.sh", `cat > '`+captured+`.tmp' && mv '`+captured+`.tmp' '`+captured+`'; printf 'SL-CACHE-RUN\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode", "cache"}}}, command: command, refreshMS: 1000})
	provider := &slProvider{usage: &qgruntime.Usage{InputTokens: 100, CachedInputTokens: 29, OutputTokens: 1}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	if frame, ok := p.waitFor(4*time.Second, containsPlain("SL-CACHE-RUN")); !ok {
		t.Fatalf("statusline command never ran:\n%s", plainFrame(frame))
	}
	p.submit("oi")
	var value any
	frame, ok := p.waitFor(6*time.Second, func(frame string) bool {
		raw, err := os.ReadFile(captured)
		if err != nil {
			return false
		}
		var doc struct {
			Cache struct {
				HitPct any `json:"hit_pct"`
			} `json:"cache"`
		}
		if json.Unmarshal(raw, &doc) != nil || doc.Cache.HitPct == nil {
			return false
		}
		value = doc.Cache.HitPct
		return strings.Contains(slFooter(plainFrame(frame)), "cache 29%")
	})
	if !ok {
		t.Fatalf("after a call with 29 of 100 input tokens cached the footer must read \"cache 29%%\" and stdin must carry hit_pct; got %v:\n%s", value, slFooter(plainFrame(frame)))
	}
	if value != float64(29) {
		t.Errorf("cache.hit_pct must be the footer's integer 29, got %v", value)
	}
}
