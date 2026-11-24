package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestUseAltScreenForInteractiveChat(t *testing.T) {
	if !useAltScreen(Options{}) {
		t.Fatal("normal chat should use the alternate screen")
	}
	if !useAltScreen(Options{Setup: SetupOptions{Visible: true}}) {
		t.Fatal("setup takeover should also use the alternate screen")
	}
}

func TestTerminalImageFrameCache(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config-root")
	cacheRoot := filepath.Join(t.TempDir(), "cache-root")
	absConfig := filepath.Join(t.TempDir(), "custom", "config.json")
	configDir := func() (string, error) { return configRoot, nil }
	cacheDir := func() (string, error) { return cacheRoot, nil }

	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "absolute config", options: Options{UserConfigPath: absConfig}, want: filepath.Join(filepath.Dir(absConfig), "images", "frame-cache")},
		{name: "relative config falls back", options: Options{UserConfigPath: "config.json"}, want: filepath.Join(configRoot, "qg", "images", "frame-cache")},
		{name: "whitespace config falls back", options: Options{UserConfigPath: "   "}, want: filepath.Join(configRoot, "qg", "images", "frame-cache")},
		{name: "empty config falls back", options: Options{}, want: filepath.Join(configRoot, "qg", "images", "frame-cache")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := terminalImageFrameCacheWith(test.options, configDir, cacheDir)
			if canonicalTestPath(t, got) != canonicalTestPath(t, test.want) {
				t.Fatalf("terminalImageFrameCacheWith() = %q, want %q", got, test.want)
			}
		})
	}

	unavailable := func() (string, error) { return "", errors.New("unavailable") }
	blank := func() (string, error) { return "", nil }
	if got, want := terminalImageFrameCacheWith(Options{}, unavailable, cacheDir), filepath.Join(cacheRoot, "qg", "images", "frame-cache"); canonicalTestPath(t, got) != canonicalTestPath(t, want) {
		t.Fatalf("cache fallback = %q, want %q", got, want)
	}
	if got, want := terminalImageFrameCacheWith(Options{}, blank, cacheDir), filepath.Join(cacheRoot, "qg", "images", "frame-cache"); canonicalTestPath(t, got) != canonicalTestPath(t, want) {
		t.Fatalf("blank config root fallback = %q, want %q", got, want)
	}
	if got := terminalImageFrameCacheWith(Options{}, unavailable, unavailable); got != "" {
		t.Fatalf("unavailable roots returned %q, want empty", got)
	}
	if got := terminalImageFrameCacheWith(Options{}, unavailable, blank); got != "" {
		t.Fatalf("blank cache root returned %q, want empty", got)
	}
}

func canonicalTestPath(t *testing.T, value string) string {
	t.Helper()
	abs, err := filepath.Abs(value)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// TestRunRejectsNonTTYStdin pins that the interactive shell fails fast with a
// non-zero code when stdin is not a terminal, instead of blocking forever in the
// Bubble Tea event loop (e.g. `echo "" | qg`). The guard runs before any model
// construction, so empty Options are fine.
func TestRunRejectsNonTTYStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	w.Close() // a pipe read-end is not a character device

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()

	done := make(chan int, 1)
	go func() { done <- Run(context.Background(), Options{}) }()

	select {
	case code := <-done:
		if code != 2 {
			t.Fatalf("Run with non-TTY stdin returned %d; want exit code 2 from the stdin TTY guard", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked on non-TTY stdin instead of failing fast")
	}
}

// Saída sem quit() (SIGHUP, SIGTERM, cancelamento do contexto, InterruptMsg):
// o modelo final ainda segura os temporários do clipboard e precisa soltá-los.
func TestExitDiscardsStagedClipboardTemps(t *testing.T) {
	dir := t.TempDir()
	staged := writeExitTestFile(t, filepath.Join(dir, "paste-1.png"))
	final := model{pendingImageTemps: []string{staged}}

	discardExitImageTemps(finalModelForExit(final))

	assertExitTestFileRemoved(t, staged)
}

// /btw: o modelo lateral zera os próprios temporários e os do pai ficam em
// btw.parent; sair com o /btw aberto tem que apagar os dois lados.
func TestExitWithBTWOpenDiscardsParentTemps(t *testing.T) {
	dir := t.TempDir()
	parentTemp := writeExitTestFile(t, filepath.Join(dir, "paste-parent.png"))
	sideTemp := writeExitTestFile(t, filepath.Join(dir, "paste-side.png"))
	parent := model{pendingImageTemps: []string{parentTemp}}
	final := model{
		pendingImageTemps: []string{sideTemp},
		btw:               btwState{active: true, parent: &parent},
	}

	discardExitImageTemps(finalModelForExit(final))

	assertExitTestFileRemoved(t, parentTemp)
	assertExitTestFileRemoved(t, sideTemp)
}

// Arquivo que o usuário anexou por caminho tem entrada "" e nunca pode ser apagado.
func TestExitKeepsUserImageFile(t *testing.T) {
	dir := t.TempDir()
	userFile := writeExitTestFile(t, filepath.Join(dir, "foto-do-usuario.png"))
	staged := writeExitTestFile(t, filepath.Join(dir, "paste-2.png"))
	final := model{
		pendingImages:      []qgruntime.ImageBlock{{}, {}},
		pendingImageLabels: []string{filepath.Base(userFile), "clipboard"},
		pendingImageTemps:  []string{"", staged},
	}

	discardExitImageTemps(finalModelForExit(final))

	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("arquivo do usuário foi apagado: %v", err)
	}
	assertExitTestFileRemoved(t, staged)
}

// finalModelForExit entrega o modelo do teste como tea.Model, do mesmo jeito que
// o program.Run entrega o modelo final em produção. O //go:noinline é o que
// garante isso: sem ele o slice de temporários "does not escape" e mora na pilha
// do teste, enquanto a conversão de model (23 KB) para a interface copia o struct
// para o heap; a pilha então cresce dentro de discardExitImageTemps e o ponteiro
// copiado envelhece, o que o -race reporta como "found bad pointer in Go heap".
// Com o retorno como interface, o compilador manda o modelo inteiro para o heap,
// como em produção.
//
//go:noinline
func finalModelForExit(m model) tea.Model { return m }

// Programa que falha antes de devolver um modelo: nil ou tipo desconhecido não pode dar panic.
func TestExitDiscardTolerantOfMissingFinalModel(t *testing.T) {
	discardExitImageTemps(nil)
	discardExitImageTemps((*model)(nil))
}

func writeExitTestFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertExitTestFileRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporário %s continua no disco (stat err=%v)", path, err)
	}
}
