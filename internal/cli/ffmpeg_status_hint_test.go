package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Sem ffmpeg, `qg ffmpeg status` mostra a orientação do sistema atual. No macOS
// o download gerenciado não existe, então citá-lo seria um beco sem saída.
func TestFFmpegStatusHintMatchesPlatform(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ffmpegStatusPlatformSeam.goos, ffmpegStatusPlatformSeam.goarch = "darwin", "arm64"
	t.Cleanup(func() { ffmpegStatusPlatformSeam.goos, ffmpegStatusPlatformSeam.goarch = "", "" })

	var stdout, stderr bytes.Buffer
	if code := runFFmpegCommand([]string{"status"}, &stdout, &stderr); code == exitSuccess {
		t.Fatal("status sem ffmpeg deveria falhar")
	}
	got := stderr.String()
	for _, want := range []string{"ffmpeg não encontrado", "brew install ffmpeg"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr deveria conter %q, foi: %q", want, got)
		}
	}
	for _, unexpected := range []string{"qg ffmpeg baixar", "winget", "apt install"} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("stderr no macOS não deveria citar %q, foi: %q", unexpected, got)
		}
	}
}
