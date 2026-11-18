package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFFmpeg registra argv e responde conforme a chamada: saída JSON de
// probe quando pedem show_entries; stderr de "ffmpeg -i" para fallback;
// cria os quadros pedidos na extração (N arquivos mínimos no padrão dado).
var fakeFFmpegScript = `#!/usr/bin/env sh
printf '%s\n' "$@" >> "$FAKE_FFMPEG_LOG"
# sleep sem herdar os pipes: senao o Output() do Go espera o EOF deles
# ate o sleep terminar, mesmo com o shell ja morto pelo contexto.
sleep "${FAKE_FFMPEG_SLEEP:-0}" </dev/null >/dev/null 2>&1
for a in "$@"; do
  if [ "$a" = "-show_entries" ]; then
    echo '{"format": {"duration": "7.300000"}, "streams": [{"width": 1920, "height": 1080}]}'
    exit 0
  fi
done
if [ "$1" = "-i" ] && [ "$#" = "2" ]; then
  echo "ffmpeg version n8.1" >&2
  echo "  Duration: 00:00:03.50, start: 0.000000, bitrate: 1 kb/s" >&2
  echo "  Stream #0:0: Video: h264, yuv420p, 640x480 [SAR 1:1 DAR 4:3], 30 fps" >&2
  exit 0
fi
frames=0
pattern=""
prev=""
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

func writeFakeFFmpeg(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fakeFFmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeReadsDurationFromFFprobeJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(dir, "argv.log"))
	ffprobe := writeFakeFFmpeg(t, dir, "ffprobe")
	touchClip(t, dir)

	info, err := Probe(context.Background(), "ffmpeg-unused", ffprobe, filepath.Join(dir, "clip.mov"))
	if err != nil {
		t.Fatalf("Probe falhou: %v", err)
	}
	if info.DurationSec < 7.29 || info.DurationSec > 7.31 {
		t.Fatalf("duração = %v, want ~7.3s", info.DurationSec)
	}
	if info.Width != 1920 || info.Height != 1080 {
		t.Fatalf("dimensões = %dx%d, want 1920x1080", info.Width, info.Height)
	}
}

func TestProbeFallsBackToFFmpegStderrWhenFFprobeMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(dir, "argv.log"))
	ffmpeg := writeFakeFFmpeg(t, dir, "ffmpeg")

	info, err := Probe(context.Background(), ffmpeg, "", filepath.Join(dir, "clip.mp4"))
	if err != nil {
		t.Fatalf("Probe com fallback falhou: %v", err)
	}
	if info.DurationSec < 3.49 || info.DurationSec > 3.51 {
		t.Fatalf("duração = %v, want ~3.5s", info.DurationSec)
	}
	if info.Width != 640 || info.Height != 480 {
		t.Fatalf("dimensões = %dx%d, want 640x480", info.Width, info.Height)
	}
}

func touchClip(t *testing.T, dir string) string {
	t.Helper()
	clip := filepath.Join(dir, "clip.mov")
	if err := os.WriteFile(clip, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return clip
}

func TestExtractFramesSpacesFramesEvenly(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	t.Setenv("FAKE_FFMPEG_LOG", log)
	ffmpeg := writeFakeFFmpeg(t, dir, "ffmpeg")
	ffprobe := writeFakeFFmpeg(t, dir, "ffprobe")
	clip := touchClip(t, dir)

	// Probe finge 7,3 s; 4 quadros em 1024 px: fps=0,5479, t=0 / 1,825 / 3,65 / 5,475.
	frames, duration, err := ExtractFrames(context.Background(), ffmpeg, ffprobe, clip,
		ExtractOptions{MaxFrames: 4, MaxSide: 1024})
	if err != nil {
		t.Fatalf("ExtractFrames falhou: %v", err)
	}
	if len(frames) != 4 {
		t.Fatalf("len(frames) = %d, want 4", len(frames))
	}
	if duration < 7.29 || duration > 7.31 {
		t.Fatalf("duração = %v, want ~7.3s", duration)
	}
	want := []float64{0, 1.825, 3.65, 5.475}
	for i, frame := range frames {
		if frame.TimestampSec < want[i]-0.02 || frame.TimestampSec > want[i]+0.02 {
			t.Fatalf("frame %d em t=%v, want t=%v", i, frame.TimestampSec, want[i])
		}
		if len(frame.Data) == 0 {
			t.Fatalf("frame %d vazio", i)
		}
	}
	argv, _ := os.ReadFile(log)
	text := string(argv)
	for _, flag := range []string{"fps=", "scale=", "-frames:v", "-q:v"} {
		if !strings.Contains(text, flag) {
			t.Fatalf("ffmpeg deveria receber %q, recebeu:\n%s", flag, text)
		}
	}
}

func TestExtractAttachmentFramesCoversVideoAndGIF(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(dir, "argv.log"))
	ffmpeg := writeFakeFFmpeg(t, dir, "ffmpeg")
	ffprobe := writeFakeFFmpeg(t, dir, "ffprobe")
	clip := touchClip(t, dir)

	frames, duration, err := ExtractAttachmentFrames(context.Background(), ffmpeg, ffprobe, clip, dir, ExtractOptions{MaxFrames: 2})
	if err != nil {
		t.Fatalf("ExtractAttachmentFrames (vídeo) falhou: %v", err)
	}
	if len(frames) != 2 || duration < 7.29 || duration > 7.31 {
		t.Fatalf("frames=%d duração=%v, want 2 e ~7.3s", len(frames), duration)
	}
	if got := SummarizeFrames("clip.mov", duration, frames); !strings.Contains(got, "Attached video: clip.mov") || !strings.Contains(got, "2 frames") {
		t.Fatalf("linha de contexto inesperada: %q", got)
	}

	gifPath := filepath.Join(dir, "anim.gif")
	if err := os.WriteFile(gifPath, tinyGIF(t, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	gframes, gduration, err := ExtractAttachmentFrames(context.Background(), "", "", gifPath, dir, ExtractOptions{MaxFrames: 2})
	if err != nil {
		t.Fatalf("ExtractAttachmentFrames (GIF) falhou: %v", err)
	}
	if len(gframes) != 2 || gduration <= 0 {
		t.Fatalf("gif: frames=%d duração=%v", len(gframes), gduration)
	}
}

func TestExtractAttachmentFramesRejectsSingleFrameGIF(t *testing.T) {
	dir := t.TempDir()
	gifPath := filepath.Join(dir, "still.gif")
	if err := os.WriteFile(gifPath, tinyGIF(t, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ExtractAttachmentFrames(context.Background(), "", "", gifPath, dir, ExtractOptions{})
	if err == nil || !strings.Contains(err.Error(), "1 quadro") {
		t.Fatalf("GIF de 1 quadro deveria voltar ao caminho de imagem, foi: %v", err)
	}
}

func TestExtractFramesRejectsOversizedVideo(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "grande.mov")
	if err := os.WriteFile(big, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ExtractFrames(context.Background(), "ffmpeg", "", big, ExtractOptions{MaxBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "grande demais") {
		t.Fatalf("vídeo acima do limite deveria recusar em português, foi: %v", err)
	}
}

func TestExtractFramesRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(dir, "argv.log"))
	t.Setenv("FAKE_FFMPEG_SLEEP", "30")
	ffmpeg := writeFakeFFmpeg(t, dir, "ffmpeg")
	ffprobe := writeFakeFFmpeg(t, dir, "ffprobe")
	clip := touchClip(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := ExtractFrames(ctx, ffmpeg, ffprobe, clip, ExtractOptions{MaxFrames: 2})
	if err == nil || !strings.Contains(err.Error(), "tempo esgotado") {
		t.Fatalf("cancelamento deveria falhar com timeout em português, foi: %v", err)
	}
}

func tinyGIF(t *testing.T, frames int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGIFSingleFrameIsNotAnimated(t *testing.T) {
	animated, err := IsAnimatedGIF(bytes.NewReader(tinyGIF(t, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if animated {
		t.Fatal("GIF de 1 quadro não é animado")
	}
}

func TestGIFAnimatedExtractsSpacedFrames(t *testing.T) {
	frames, duration, err := ExtractGIFFrames(tinyGIF(t, 5), 3, 64)
	if err != nil {
		t.Fatalf("ExtractGIFFrames falhou: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("len(frames) = %d, want 3", len(frames))
	}
	if duration <= 0 {
		t.Fatalf("duração deveria ser positiva, foi %v", duration)
	}
	for _, frame := range frames {
		if len(frame.Data) == 0 {
			t.Fatal("quadro vazio")
		}
	}
}

func TestExtractFramesRealFFmpegWhenAvailable(t *testing.T) {
	ffmpeg, ffprobe, ok := LookPathFFmpeg()
	if !ok {
		t.Skip("sem ffmpeg no PATH, pulando integração real")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "Gravação de Tela 01.mp4")
	gen := exec.CommandContext(context.Background(), ffmpeg,
		"-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("gerar vídeo de teste falhou: %v\n%s", err, out)
	}
	info, err := Probe(context.Background(), ffmpeg, ffprobe, src)
	if err != nil {
		t.Fatalf("Probe no vídeo real falhou: %v", err)
	}
	if info.DurationSec < 1.9 || info.DurationSec > 2.1 {
		t.Fatalf("duração = %v, want ~2s", info.DurationSec)
	}
	if info.Width != 320 || info.Height != 240 {
		t.Fatalf("dimensões = %dx%d, want 320x240", info.Width, info.Height)
	}
	frames, _, err := ExtractFrames(context.Background(), ffmpeg, ffprobe, src, ExtractOptions{MaxFrames: 4, MaxSide: 256})
	if err != nil {
		t.Fatalf("ExtractFrames no vídeo real falhou: %v", err)
	}
	if len(frames) < 2 || len(frames) > 4 {
		t.Fatalf("len(frames) = %d, want 2..4", len(frames))
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].TimestampSec <= frames[i-1].TimestampSec {
			t.Fatalf("instantes fora de ordem: %v", frames[i].TimestampSec)
		}
	}
}

func TestExtractFramesKeepsSpacedAccentPathIntact(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	t.Setenv("FAKE_FFMPEG_LOG", log)
	ffmpeg := writeFakeFFmpeg(t, dir, "ffmpeg")
	ffprobe := writeFakeFFmpeg(t, dir, "ffprobe")
	clip := filepath.Join(dir, "Gravação de Tela 01.mov")
	if err := os.WriteFile(clip, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	frames, _, err := ExtractFrames(context.Background(), ffmpeg, ffprobe, clip, ExtractOptions{MaxFrames: 2, MaxSide: 256})
	if err != nil {
		t.Fatalf("ExtractFrames com acento falhou: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("nenhum quadro extraído")
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), clip+"\n") {
		t.Fatalf("caminho com espaço/acento deveria chegar intacto ao ffmpeg, argv:\n%s", argv)
	}
}
