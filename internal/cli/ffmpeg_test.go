package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFFmpegStatusWithoutBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runFFmpegCommand([]string{"status"}, &stdout, &stderr); code == exitSuccess {
		t.Fatal("status sem ffmpeg deveria falhar")
	}
	for _, want := range []string{"ffmpeg não encontrado", "qg ffmpeg baixar", "brew install ffmpeg"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr deveria conter %q, foi: %q", want, stderr.String())
		}
	}
}

func TestFFmpegStatusShowsSystemBinary(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"ffmpeg version n8.1-test\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var stdout, stderr bytes.Buffer
	if code := runFFmpegCommand([]string{"status"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("status com ffmpeg no PATH falhou: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), fake) {
		t.Fatalf("stdout deveria mostrar o caminho %s, foi: %s", fake, stdout.String())
	}
}

func TestFFmpegBaixarRejectsTamperedBundle(t *testing.T) {
	// Fixa uma plataforma com pacote (linux/amd64) para o teste não depender
	// do host: no darwin/arm64 não há build fixado e o comando falharia com
	// "sem suporte" antes de chegar à verificação SHA-256.
	ffmpegPlatformSeam.goos, ffmpegPlatformSeam.goarch = "linux", "amd64"
	t.Cleanup(func() { ffmpegPlatformSeam.goos, ffmpegPlatformSeam.goarch = "", "" })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bytes-adulterados"))
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	root := t.TempDir()
	code := runFFmpegCommand([]string{"baixar", "--root", root, "--base-url", server.URL}, &stdout, &stderr)
	if code == exitSuccess {
		t.Fatal("baixar com hash divergente deveria falhar")
	}
	if !strings.Contains(stderr.String(), "SHA-256") {
		t.Fatalf("erro deveria citar a verificação SHA-256, foi: %s", stderr.String())
	}
}

func TestFFmpegBaixarUnsupportedPlatformPointsToManualInstall(t *testing.T) {
	// O outro lado do seam: numa plataforma sem pacote fixado (darwin),
	// o comando continua recusando com a orientação de instalação manual.
	ffmpegPlatformSeam.goos, ffmpegPlatformSeam.goarch = "darwin", "arm64"
	t.Cleanup(func() { ffmpegPlatformSeam.goos, ffmpegPlatformSeam.goarch = "", "" })

	var stdout, stderr bytes.Buffer
	code := runFFmpegCommand([]string{"baixar", "--root", t.TempDir()}, &stdout, &stderr)
	if code == exitSuccess {
		t.Fatal("baixar sem pacote fixado deveria falhar")
	}
	for _, want := range []string{"não tem suporte", "darwin/arm64", "brew install ffmpeg"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("erro deveria citar %q, foi: %s", want, stderr.String())
		}
	}
}

func TestFFmpegHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runFFmpegCommand([]string{"--help"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("--help falhou: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "qg ffmpeg [status|baixar]") {
		t.Fatalf("ajuda incompleta:\n%s", stdout.String())
	}
}

func TestFFmpegUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runFFmpegCommand([]string{"frobnicate"}, &stdout, &stderr); code == exitSuccess {
		t.Fatal("subcomando inválido deveria falhar")
	}
}

func TestCompletionsIncludeFFmpeg(t *testing.T) {
	contexts := completionContexts(completionRoot)
	byPath := make(map[string][]string, len(contexts))
	for _, context := range contexts {
		byPath[context.path] = context.candidates
	}
	assertCandidates(t, byPath[""], "ffmpeg")
	assertCandidates(t, byPath["ffmpeg"], "status", "baixar")
}
