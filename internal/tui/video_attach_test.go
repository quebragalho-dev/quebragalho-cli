package tui

import (
	"context"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFFmpegTUI responde probe (show_entries) e cria os quadros pedidos,
// como um ffmpeg real faria — sem rede, sem binário de verdade.
const fakeFFmpegTUI = `#!/bin/sh
printf '%s\n' "$@" >> "$FAKE_FFMPEG_LOG"
for a in "$@"; do
  if [ "$a" = "-show_entries" ]; then
    echo '{"format": {"duration": "7.300000"}, "streams": [{"width": 1920, "height": 1080}]}'
    exit 0
  fi
done
frames=0
pattern=""
for a in "$@"; do
  if [ "$prev" = "-frames:v" ]; then frames="$a"; fi
  prev="$a"
  pattern="$a"
done
if [ "$frames" -gt 0 ] 2>/dev/null; then
  i=1
  while [ "$i" -le "$frames" ]; do
    # Sem sed de propósito: o teste pode rodar com PATH só com o falso.
    out=$(printf "$pattern" "$i")
    printf '\377\330\377\331' > "$out"
    i=$((i + 1))
  done
fi
exit 0
`

func writeFakeFFmpegTUI(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg em shell não roda no Windows; extração real é coberta pelo Maestro")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fakeFFmpegTUI), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTinyGIF(t *testing.T, dir, name string, frames int) string {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 8, 8), palette))
		g.Delay = append(g.Delay, 10)
	}
	var buf strings.Builder
	_ = buf
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gif.EncodeAll(file, g); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	return path
}

func videoAttachModel(t *testing.T, root string) model {
	t.Helper()
	return newModel(context.Background(), Options{Cwd: root, ModelName: "muse-spark-1.3-contributor"})
}

// Anexar vídeo com ffmpeg disponível vira quadros + linha de contexto.
func TestVideoAttachStagesFramesWithContextLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "Gravacao de Tela 01.mov")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := videoAttachModel(t, root)
	next := m.handleImageCommand(video)
	if len(next.pendingImages) != 8 {
		t.Fatalf("quadros anexados = %d, want 8 (padrão)", len(next.pendingImages))
	}
	if len(next.pendingVideoInfos) != 1 {
		t.Fatalf("infos de vídeo = %d, want 1", len(next.pendingVideoInfos))
	}
	text := next.pendingVideoInfos[0].text
	for _, want := range []string{"Attached video:", "Gravacao de Tela 01.mov", "7.3s", "8 frames", "t=0.0s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("linha de contexto sem %q: %q", want, text)
		}
	}
	if next.pendingImageThumbnails == nil {
		t.Fatal("prévia do primeiro quadro deveria existir")
	}
}

// Sem visão, anexar vídeo recusa em português.
func TestVideoAttachRefusesWithoutVision(t *testing.T) {
	root := t.TempDir()
	m := newModel(context.Background(), Options{Cwd: root, ModelName: "totally-unknown-custom"})
	next := m.handleImageCommand(filepath.Join(root, "filme.mp4"))
	if len(next.pendingImages) != 0 {
		t.Fatal("sem visão, nada deveria anexar")
	}
	want := "O modelo totally-unknown-custom não suporta imagens; anexo de vídeo recusado."
	if notice := lastTranscriptText(next); notice != want {
		t.Fatalf("aviso = %q, want %q", notice, want)
	}
}

// Sem ffmpeg (nem no PATH nem gerenciado), o aviso explica como instalar.
func TestVideoAttachWithoutFFmpegExplainsInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	video := filepath.Join(root, "filme.mp4")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := videoAttachModel(t, root)
	next := m.handleImageCommand(video)
	if len(next.pendingImages) != 0 {
		t.Fatal("sem ffmpeg, nada deveria anexar")
	}
	notice := lastTranscriptText(next)
	for _, want := range []string{"ffmpeg não encontrado", "qg ffmpeg baixar", "brew install ffmpeg"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("aviso deveria conter %q, foi: %q", want, notice)
		}
	}
}

// GIF animado não precisa de ffmpeg: vira quadros direto.
func TestAnimatedGIFAttachesFramesWithoutFFmpeg(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gifPath := writeTinyGIF(t, root, "animado.gif", 5)

	m := videoAttachModel(t, root)
	next := m.handleImageCommand(gifPath)
	if len(next.pendingImages) == 0 {
		t.Fatalf("GIF animado deveria anexar quadros, aviso: %q", lastTranscriptText(next))
	}
	if len(next.pendingVideoInfos) != 1 {
		t.Fatal("GIF animado deveria gerar linha de contexto")
	}
}

// GIF de 1 quadro segue o caminho normal de imagem.
func TestSingleFrameGIFStaysImage(t *testing.T) {
	root := t.TempDir()
	gifPath := writeTinyGIF(t, root, "estatico.gif", 1)

	m := videoAttachModel(t, root)
	next := m.handleImageCommand(gifPath)
	if len(next.pendingImages) != 1 {
		t.Fatalf("GIF de 1 quadro deveria anexar 1 imagem, foi %d (%q)", len(next.pendingImages), lastTranscriptText(next))
	}
	if len(next.pendingVideoInfos) != 0 {
		t.Fatal("GIF de 1 quadro não é vídeo")
	}
}

// Arrastar/soltar reconhece contêineres de vídeo.
func TestDroppedVideoPathRecognized(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "aula.MP4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := droppedAttachmentPath(video, root); !ok {
		t.Fatal("caminho .MP4 deveria ser reconhecido como anexo")
	}
}

// A linha de contexto entra no prompt no envio e limpa o pendente.
func TestConsumePendingVideosPreamble(t *testing.T) {
	m := videoAttachModel(t, t.TempDir())
	m.pendingVideoInfos = []pendingVideoInfo{{label: "a.mov", text: "Attached video: a.mov", frames: 2}}
	got := m.consumePendingVideos()
	if !strings.Contains(got, "Attached video: a.mov") {
		t.Fatalf("preâmbulo = %q", got)
	}
	if len(m.pendingVideoInfos) != 0 {
		t.Fatal("consumir deveria limpar os pendentes")
	}
	if got := m.consumePendingVideos(); got != "" {
		t.Fatalf("segunda chamada deveria ser vazia, foi %q", got)
	}
}

// Quadros de vídeo entram pelo mesmo caminho das imagens: temps em lockstep
// ("" porque o quadro vem de arquivo real, não de temporário).
func TestVideoAttachKeepsTempsInLockstep(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "filme.mp4")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := videoAttachModel(t, root).handleImageCommand(video)
	if len(next.pendingImages) == 0 {
		t.Fatalf("setup: nada anexado (%q)", lastTranscriptText(next))
	}
	if len(next.pendingImageTemps) != len(next.pendingImages) {
		t.Fatalf("temps fora de lockstep: %d temps para %d imagens", len(next.pendingImageTemps), len(next.pendingImages))
	}
	for _, temp := range next.pendingImageTemps {
		if temp != "" {
			t.Fatalf("quadro de vídeo não tem temporário próprio, foi %q", temp)
		}
	}
}

// Remover anexo tira o vídeo inteiro (todos os quadros) de uma vez.
func TestRemoveLastAttachmentDropsWholeVideo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "filme.mp4")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := videoAttachModel(t, root)
	staged := m.handleImageCommand(video)
	if len(staged.pendingImages) == 0 {
		t.Fatalf("setup: nada anexado (%q)", lastTranscriptText(staged))
	}
	removed, ok := staged.removeLastAttachment()
	if !ok {
		t.Fatal("removeLastAttachment deveria remover o vídeo")
	}
	if len(removed.pendingImageTemps) != len(removed.pendingImages) {
		t.Fatalf("temps fora de lockstep após remover vídeo: %d temps, %d imagens",
			len(removed.pendingImageTemps), len(removed.pendingImages))
	}
	if len(removed.pendingImages) != 0 || len(removed.pendingVideoInfos) != 0 {
		t.Fatalf("vídeo deveria sair inteiro: %d imagens, %d infos",
			len(removed.pendingImages), len(removed.pendingVideoInfos))
	}
}
