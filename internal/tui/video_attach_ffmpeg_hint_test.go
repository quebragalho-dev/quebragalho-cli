package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// O aviso de ffmpeg ausente mostra só a orientação do sistema atual, com o
// verbo da ação. No macOS não existe pacote fixado, então "qg ffmpeg baixar"
// não pode aparecer; no Linux e no Windows ele entra junto da alternativa do
// gerenciador de pacotes. O GOOS é injetado para o teste não depender do host.
func TestVideoAttachFFmpegHintMatchesPlatform(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		goarch      string
		want        string
		notExpected []string
	}{
		{
			name: "macOS só Homebrew", goos: "darwin", goarch: "arm64",
			want:        "ffmpeg não encontrado para extrair os quadros do vídeo. Instale com: brew install ffmpeg.",
			notExpected: []string{"qg ffmpeg baixar", "winget", "apt install"},
		},
		{
			name: "Linux baixa ou apt", goos: "linux", goarch: "amd64",
			want:        "ffmpeg não encontrado para extrair os quadros do vídeo. Baixe com: qg ffmpeg baixar (download único, ~115–135 MB) ou instale com: sudo apt install ffmpeg.",
			notExpected: []string{"brew", "winget"},
		},
		{
			name: "Windows baixa ou winget", goos: "windows", goarch: "amd64",
			want:        "ffmpeg não encontrado para extrair os quadros do vídeo. Baixe com: qg ffmpeg baixar (download único, ~115–170 MB) ou instale com: winget install Gyan.FFmpeg.",
			notExpected: []string{"brew", "apt install"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("PATH", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			video := filepath.Join(root, "filme.mp4")
			if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}

			tuiFFmpegGOOS, tuiFFmpegGOARCH = tc.goos, tc.goarch
			t.Cleanup(func() {
				tuiFFmpegGOOS, tuiFFmpegGOARCH = runtime.GOOS, runtime.GOARCH
			})

			next, _ := videoAttachModel(t, root).handleImageCommand(video)
			notice := lastTranscriptText(next)
			if !strings.Contains(notice, tc.want) {
				t.Fatalf("aviso = %q, want conter %q", notice, tc.want)
			}
			for _, unexpected := range tc.notExpected {
				if strings.Contains(notice, unexpected) {
					t.Fatalf("aviso = %q, não deveria citar %q", notice, unexpected)
				}
			}
		})
	}
}
