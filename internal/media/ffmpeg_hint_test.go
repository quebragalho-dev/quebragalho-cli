package media

import (
	"runtime"
	"strings"
	"testing"
)

// O aviso de ffmpeg ausente listava os três sistemas de uma vez e mandava
// "qg ffmpeg baixar" até no macOS, onde não existe pacote fixado. A orientação
// agora é só a do sistema atual, e o download só entra onde ele funciona.
func TestFFmpegMissingHintPerPlatform(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		goarch      string
		want        string
		notExpected []string
	}{
		{
			name: "macOS instala pelo Homebrew",
			goos: "darwin", goarch: "arm64",
			want: "brew install ffmpeg",
			// O download não é suportado no darwin: citá-lo seria mentira.
			notExpected: []string{"qg ffmpeg baixar", "apt install", "winget"},
		},
		{
			name: "Linux baixa ou usa o apt",
			goos: "linux", goarch: "amd64",
			want:        "qg ffmpeg baixar (download único, ~115–135 MB) ou sudo apt install ffmpeg",
			notExpected: []string{"brew", "winget"},
		},
		{
			name: "Windows baixa ou usa o winget",
			goos: "windows", goarch: "amd64",
			want:        "qg ffmpeg baixar (~115–170 MB) ou winget install Gyan.FFmpeg",
			notExpected: []string{"brew", "apt install"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := FFmpegMissingHint(tc.goos, tc.goarch)
			if !strings.Contains(hint, tc.want) {
				t.Fatalf("aviso = %q, want conter %q", hint, tc.want)
			}
			for _, unexpected := range tc.notExpected {
				if strings.Contains(hint, unexpected) {
					t.Fatalf("aviso = %q, não deveria citar %q", hint, unexpected)
				}
			}
		})
	}
}

// O download só é oferecido quando existe pacote fixado para o par atual: é
// media.FFmpegAssetFor que decide, não um switch de GOOS paralelo.
func TestFFmpegMissingHintOffersDownloadOnlyWithPinnedAsset(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			_, pinned := FFmpegAssetFor(goos, goarch)
			hint := FFmpegMissingHint(goos, goarch)
			offers := strings.Contains(hint, "qg ffmpeg baixar")
			if offers != pinned {
				t.Fatalf("%s/%s: aviso oferece download = %v, want %v (%q)", goos, goarch, offers, pinned, hint)
			}
		}
	}
}

// A dica do host tem que ser a do host: a produção chama com runtime.GOOS.
func TestFFmpegMissingHintForHostIsNonEmpty(t *testing.T) {
	if hint := FFmpegMissingHint(runtime.GOOS, runtime.GOARCH); hint == "" {
		t.Fatal("aviso vazio para a plataforma do host")
	}
}
