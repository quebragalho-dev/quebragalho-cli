package tui

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"

	"github.com/quebragalho-dev/quebragalho-cli/internal/media"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
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

// attachVideoAndFinish roda /image num vídeo e espera a extração assíncrona
// terminar, como o Update faria com o tea.Cmd devolvido.
func attachVideoAndFinish(t *testing.T, m model, path string) model {
	t.Helper()
	next, cmd := m.handleImageCommand(path)
	if cmd == nil {
		t.Fatal("anexo de vídeo deveria extrair em background (tea.Cmd)")
	}
	msg, ok := execCmd(cmd).(videoFramesMsg)
	if !ok {
		t.Fatalf("esperava videoFramesMsg, veio %T", msg)
	}
	updated, _ := next.Update(msg)
	return updated.(model)
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
	next := attachVideoAndFinish(t, m, video)
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
	next, cmd := m.handleImageCommand(filepath.Join(root, "filme.mp4"))
	if cmd != nil {
		t.Fatal("recusa não deveria agendar extração")
	}
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
	next, cmd := m.handleImageCommand(video)
	if cmd != nil {
		t.Fatal("sem ffmpeg, nada deveria ser agendado")
	}
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
	next, cmd := m.handleImageCommand(gifPath)
	if cmd != nil {
		t.Fatal("GIF deveria anexar de forma síncrona")
	}
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
	next, cmd := m.handleImageCommand(gifPath)
	if cmd != nil {
		t.Fatal("GIF deveria anexar de forma síncrona")
	}
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
	next := attachVideoAndFinish(t, videoAttachModel(t, root), video)
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
// Sem visão no envio, a linha de contexto não vai sozinha: o drop dos
// quadros leva a infos junto.
func TestVideoPreambleDroppedWithoutVision(t *testing.T) {
	m := newModel(context.Background(), Options{Cwd: t.TempDir(), ModelName: "totally-unknown-custom"})
	m.pendingVideoInfos = []pendingVideoInfo{{label: "a.mov", text: "Attached video: a.mov", frames: 2}}
	if got := m.consumePendingVideoPreamble(); got != "" {
		t.Fatalf("preâmbulo sem visão deveria ser vazio, foi %q", got)
	}
	if len(m.pendingVideoInfos) != 0 {
		t.Fatal("infos deveriam ser descartadas junto com os quadros")
	}

	vision := newModel(context.Background(), Options{Cwd: t.TempDir(), ModelName: "muse-spark-1.3-contributor"})
	vision.pendingVideoInfos = []pendingVideoInfo{{label: "a.mov", text: "Attached video: a.mov", frames: 2}}
	if got := vision.consumePendingVideoPreamble(); got != "Attached video: a.mov\n" {
		t.Fatalf("preâmbulo com visão = %q", got)
	}
}

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
	staged := attachVideoAndFinish(t, m, video)
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

// /image clear durante a extração descarta os quadros tardios.
func TestClearDuringExtractionDropsLateFrames(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, _ := m.handleImageCommand(video)
	wantID := attached.videoExtractSeq
	if wantID == 0 {
		t.Fatal("setup: extração deveria estar registrada")
	}
	cleared, _ := attached.handleImageCommand("clear")
	stale := videoFramesMsg{id: wantID, label: "clip.mp4", frames: []media.Frame{{Data: []byte("x")}}, duration: 1}
	final, _ := cleared.Update(stale)
	got := final.(model)
	if len(got.pendingImages) != 0 || len(got.pendingVideoInfos) != 0 {
		t.Fatal("quadros tardios deveriam ser descartados após /image clear")
	}
}

// Backspace durante a extração cancela o envio e descarta os quadros tardios.
func TestBackspaceDuringExtractionCancelsIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, _ := m.handleImageCommand(video)
	wantID := attached.videoExtractSeq
	if wantID == 0 {
		t.Fatal("setup: extração em andamento deveria estar registrada")
	}
	without, removed := attached.removeLastAttachment()
	if !removed {
		t.Fatal("Backspace deveria cancelar a extração em andamento")
	}
	stale := videoFramesMsg{id: wantID, label: "clip.mp4", frames: []media.Frame{{Data: []byte("x")}}, duration: 1}
	final, _ := without.Update(stale)
	if got := final.(model); len(got.pendingImages) != 0 {
		t.Fatal("quadros tardios deveriam ser descartados após cancelar")
	}
}

// Com extração em andamento, Enter não envia: mantém o rascunho e avisa.
func TestSubmitWaitsForInFlightVideoExtraction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, _ := m.handleImageCommand(video)
	if _, _, busy := attached.newestPendingVideoExtraction(); !busy {
		t.Fatal("setup: extração deveria estar em andamento")
	}
	attached.input.SetValue("o que mostra o vídeo?")
	submitted, cmd := attached.handleSubmit()
	if cmd != nil {
		t.Fatal("submit durante extração não deveria despachar turno")
	}
	sub := submitted.(model)
	if got := sub.composerValue(); got != "o que mostra o vídeo?" {
		t.Fatalf("rascunho deveria ser mantido, foi %q", got)
	}
	if notice := lastTranscriptText(sub); !strings.Contains(notice, "Aguarde: extraindo quadros de") {
		t.Fatalf("aviso de espera ausente, veio: %q", notice)
	}
}

// Ctrl+Enter (sendNow) com extração em andamento não despacha.
func TestSendNowWaitsForInFlightVideoExtraction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, _ := m.handleImageCommand(video)
	next, cmd, _ := attached.sendNow("descreva")
	if cmd != nil {
		t.Fatal("sendNow durante extração não deveria despachar turno")
	}
	if notice := lastTranscriptText(next); !strings.Contains(notice, "Aguarde: extraindo quadros de") {
		t.Fatalf("aviso de espera ausente, veio: %q", notice)
	}
}

// Fila automática não drena enquanto extrai: a mensagem espera pelos quadros.
func TestQueuedDrainWaitsForInFlightVideoExtraction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, _ := m.handleImageCommand(video)
	attached.queuedMessages = []string{"pergunta na fila"}
	next, cmd := attached.launchQueuedMessageIfReady()
	if cmd != nil {
		t.Fatal("dreno automático não deveria despachar durante a extração")
	}
	if len(next.queuedMessages) != 1 {
		t.Fatal("fila deveria ser mantida intacta")
	}
}

// videoBTWModel monta um modelo com sessão ativa (para /btw funcionar) e
// visão (para anexar vídeo), com o cwd no diretório do teste.
func videoBTWModel(t *testing.T, root string) model {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	releaseSessionLocksUnder(t, store.RootDir)
	parent, err := store.Create(sessions.CreateInput{
		SessionID: "video-main-session",
		Title:     "tarefa com vídeo",
		Cwd:       root,
		ModelID:   "muse-spark-1.3-contributor",
		Provider:  "test-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(parent.SessionID)
	if err != nil || loaded == nil {
		t.Fatalf("load parent session: %v", err)
	}
	m := newModel(context.Background(), Options{
		Cwd:          root,
		ModelName:    "muse-spark-1.3-contributor",
		ProviderName: "test-provider",
		SessionStore: store,
	})
	m.activeSession = *loaded
	return m
}

// /btw durante a extração do pai não cancela: os quadros chegam e entram
// na fila do pai.
func TestBTWDoesNotCancelParentVideoExtraction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoBTWModel(t, root)
	attached, cmd := m.handleImageCommand(video)
	if cmd == nil {
		t.Fatal("setup: extração deveria estar em andamento")
	}
	var wantID uint64
	for id := range attached.pendingVideoExtractions {
		wantID = id
	}
	if wantID == 0 {
		t.Fatal("setup: extração deveria estar registrada")
	}
	side, _ := attached.handleBTWCommand("")
	if !side.btw.active {
		t.Fatalf("setup: btw deveria estar ativo, aviso: %q", lastTranscriptText(side))
	}
	if _, ok := attached.pendingVideoExtractions[wantID]; !ok {
		t.Fatal("/btw cancelou a extração do pai")
	}
	msg, ok := execCmd(cmd).(videoFramesMsg)
	if !ok {
		t.Fatalf("esperava videoFramesMsg, veio %T", msg)
	}
	updated, _ := side.Update(msg)
	returned, _ := updated.(model).leaveBTW()
	if len(returned.pendingImages) == 0 || len(returned.pendingVideoInfos) == 0 {
		t.Fatalf("quadros deveriam entrar na fila do pai: %d imagens, %d infos",
			len(returned.pendingImages), len(returned.pendingVideoInfos))
	}
}

// Fila parada na extração anda sozinha quando os quadros chegam.
func TestQueuedDrainRunsAfterVideoFramesArrive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	video := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := videoAttachModel(t, root)
	attached, cmd := m.handleImageCommand(video)
	if cmd == nil {
		t.Fatal("setup: extração deveria estar em andamento")
	}
	attached.queuedMessages = []string{"descreva o vídeo"}
	msg, ok := execCmd(cmd).(videoFramesMsg)
	if !ok {
		t.Fatalf("esperava videoFramesMsg, veio %T", msg)
	}
	updated, _ := attached.Update(msg)
	got := updated.(model)
	if len(got.queuedMessages) != 0 {
		t.Fatal("a fila deveria ter andado com a chegada dos quadros")
	}
	if len(got.pendingImages) == 0 {
		t.Fatalf("quadros deveriam ter sido anexados, aviso: %q", lastTranscriptText(got))
	}
}

// Um vídeo anexado aparece no composer como um só chip com o número de
// quadros, em vez de um chip [Image #N] por quadro.
func TestComposerCollapsesVideoFramesIntoSingleChip(t *testing.T) {
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

	next := attachVideoAndFinish(t, videoAttachModel(t, root), video)
	if got := next.attachmentThumbnailSupplementalChips(); got != "[Vídeo #1 · 8 quadros]" {
		t.Fatalf("composer deveria mostrar um só chip de vídeo, foi %q", got)
	}
}

// GIF animado aparece como um só chip [GIF #N · K quadros].
func TestComposerLabelsAnimatedGIFChip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gifPath := writeTinyGIF(t, root, "animado.gif", 5)

	m := videoAttachModel(t, root)
	next, cmd := m.handleImageCommand(gifPath)
	if cmd != nil {
		t.Fatal("GIF deveria anexar de forma síncrona")
	}
	if got := next.attachmentThumbnailSupplementalChips(); got != "[GIF #1 · 5 quadros]" {
		t.Fatalf("composer deveria mostrar um só chip de GIF, foi %q", got)
	}
}

// Imagem + vídeo + imagem: as avulsas numeram sem contar os quadros.
func TestComposerNumbersLooseImagesAroundVideo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	writeTestPNG(t, root, "antes.png")
	video := filepath.Join(root, "filme.mp4")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, root, "depois.png")

	m := videoAttachModel(t, root)
	first, _ := m.handleImageCommand(filepath.Join(root, "antes.png"))
	staged := attachVideoAndFinish(t, first, video)
	next, _ := staged.handleImageCommand(filepath.Join(root, "depois.png"))
	if got := next.attachmentThumbnailSupplementalChips(); got != "[Image #1] [Vídeo #1 · 8 quadros] [Image #2]" {
		t.Fatalf("mistura imagem+vídeo+imagem = %q", got)
	}
}

// Backspace tira o vídeo inteiro de uma vez, mantendo as imagens avulsas.
func TestBackspaceRemovesWholeVideoFromMixedAttachments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(root, "argv.log"))
	writeFakeFFmpegTUI(t, root, "ffmpeg")
	writeFakeFFmpegTUI(t, root, "ffprobe")
	writeTestPNG(t, root, "antes.png")
	writeTestPNG(t, root, "meio.png")
	video := filepath.Join(root, "filme.mp4")
	if err := os.WriteFile(video, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := videoAttachModel(t, root)
	first, _ := m.handleImageCommand(filepath.Join(root, "antes.png"))
	second, _ := first.handleImageCommand(filepath.Join(root, "meio.png"))
	staged := attachVideoAndFinish(t, second, video)
	if got := staged.attachmentThumbnailSupplementalChips(); got != "[Image #1] [Image #2] [Vídeo #1 · 8 quadros]" {
		t.Fatalf("setup: mistura = %q", got)
	}

	withoutVideo, ok := staged.removeLastAttachment()
	if !ok {
		t.Fatal("Backspace deveria remover o vídeo")
	}
	if len(withoutVideo.pendingImages) != 2 || len(withoutVideo.pendingVideoInfos) != 0 {
		t.Fatalf("vídeo deveria sair inteiro: %d imagens, %d infos",
			len(withoutVideo.pendingImages), len(withoutVideo.pendingVideoInfos))
	}
	if got := withoutVideo.attachmentThumbnailSupplementalChips(); got != "[Image #1] [Image #2]" {
		t.Fatalf("restaram as imagens avulsas renumeradas, chips = %q", got)
	}
}

// No envio, a linha do transcript mostra um só chip por vídeo.
func TestTranscriptShowsSingleVideoChipAfterSubmit(t *testing.T) {
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

	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "ok"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		Cwd:          root,
		ProviderName: "openai",
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.agentOptions.OnText = func(string) {}

	staged := attachVideoAndFinish(t, m, video)
	staged.input.SetValue("descreva o vídeo")
	submitted, cmd := staged.handleSubmit()
	if cmd == nil {
		t.Fatalf("submit deveria despachar o turno (%q)", lastTranscriptText(submitted.(model)))
	}
	updated, _ := submitted.(model).Update(execCmd(cmd))
	final := updated.(model)
	settleModel(t, final)

	var found transcriptAttachmentSummary
	for _, row := range final.transcript {
		if row.kind == rowUser && !row.attachments.empty() {
			found = row.attachments
		}
	}
	if got := renderUserAttachmentSummary(found); got != "[Vídeo #1 · 8 quadros]" {
		t.Fatalf("transcript deveria mostrar um só chip de vídeo, foi %q", got)
	}
	// O que vai ao modelo não muda: os 8 quadros seguem como imagens.
	if len(provider.requests) != 1 {
		t.Fatalf("esperava um request, foram %d", len(provider.requests))
	}
	last := provider.requests[0].Messages[len(provider.requests[0].Messages)-1]
	if len(last.Images) != 8 {
		t.Fatalf("modelo deveria receber os 8 quadros, recebeu %d imagens", len(last.Images))
	}
	if !strings.Contains(last.Content, "Attached video: filme.mp4") {
		t.Fatalf("linha de contexto do vídeo deveria ir ao modelo, foi: %q", last.Content)
	}
}

// A passada do composer agrupa os quadros contíguos de cada vídeo num só
// chip; entradas fora do padrão continuam como imagens avulsas.
func TestRenderAttachmentChipsVideoRuns(t *testing.T) {
	labels := []string{"antes.png", "clip.mp4", "clip.mp4", "clip.mp4", "depois.png"}
	videos := []pendingVideoInfo{{label: "clip.mp4", text: "Attached video: clip.mp4", frames: 3}}
	if got := renderAttachmentChips(labels, nil, videos); got != "[Image #1] [Vídeo #1 · 3 quadros] [Image #2]" {
		t.Fatalf("chips = %q", got)
	}

	if got := renderAttachmentChips([]string{"clip.mp4"}, nil,
		[]pendingVideoInfo{{label: "clip.mp4", frames: 1}}); got != "[Vídeo #1 · 1 quadro]" {
		t.Fatalf("singular = %q", got)
	}

	// Vídeo e GIF numeram separado, na ordem de anexo.
	mixed := []string{"clip.mp4", "clip.mp4", "anim.gif", "anim.gif", "anim.gif"}
	mixedVideos := []pendingVideoInfo{
		{label: "clip.mp4", frames: 2},
		{label: "anim.gif", frames: 3, gif: true},
	}
	if got := renderAttachmentChips(mixed, nil, mixedVideos); got != "[Vídeo #1 · 2 quadros] [GIF #1 · 3 quadros]" {
		t.Fatalf("mistura vídeo+GIF = %q", got)
	}

	// Info sem quadros (extração vazia) não rende chip.
	if got := renderAttachmentChips(nil, nil, []pendingVideoInfo{{label: "vazio.mp4", frames: 0}}); got != "" {
		t.Fatalf("info vazia deveria sumir, foi %q", got)
	}
}

// O resumo do transcript agrupa imagens, vídeos e documentos com o mesmo
// teto de visibilidade de antes.
func TestRenderUserAttachmentSummaryWithVideos(t *testing.T) {
	summary := transcriptAttachmentSummary{
		images:    2,
		documents: 1,
		videos:    []videoAttachmentSummary{{frames: 8}, {frames: 5, gif: true}},
	}
	if got := renderUserAttachmentSummary(summary); got != "[Image #1] [Image #2] [Vídeo #1 · 8 quadros] [GIF #1 · 5 quadros] [Document #1]" {
		t.Fatalf("resumo = %q", got)
	}

	overflow := transcriptAttachmentSummary{
		images:    5,
		documents: 5,
		videos: []videoAttachmentSummary{
			{frames: 1}, {frames: 2}, {frames: 3}, {frames: 4}, {frames: 5},
		},
	}
	if got := renderUserAttachmentSummary(overflow); got != "[Image #1] [Image #2] [Image #3] [Image #4] [Vídeo #1 · 1 quadro] [Vídeo #2 · 2 quadros] [Vídeo #3 · 3 quadros] [Vídeo #4 · 4 quadros] [Document #1] [Document #2] [Document #3] [Document #4] [+3 attachments]" {
		t.Fatalf("transbordo = %q", got)
	}

	if got := renderUserAttachmentSummary(transcriptAttachmentSummary{}); got != "" {
		t.Fatalf("resumo vazio deveria ser vazio, foi %q", got)
	}
}

// O relato da sessão leva os vídeos (quadros + GIF) e volta igual; relatos
// antigos sem a chave e entradas malformadas não quebram a leitura.
func TestAttachmentSummaryPayloadRoundTripsVideos(t *testing.T) {
	in := transcriptAttachmentSummary{
		images:    1,
		documents: 1,
		videos:    []videoAttachmentSummary{{frames: 8}, {frames: 3, gif: true}},
	}
	raw, err := json.Marshal(map[string]any{"attachments": attachmentSummaryPayload(in)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	back := attachmentSummaryFromPayload(decoded)
	if back.images != 1 || back.documents != 1 || len(back.videos) != 2 ||
		back.videos[0] != (videoAttachmentSummary{frames: 8}) ||
		back.videos[1] != (videoAttachmentSummary{frames: 3, gif: true}) {
		t.Fatalf("volta diferente: %#v", back)
	}

	old := attachmentSummaryFromPayload(map[string]any{
		"attachments": map[string]any{"images": 2.0, "documents": 1.0},
	})
	if len(old.videos) != 0 || old.images != 2 || old.documents != 1 {
		t.Fatalf("relato antigo deveria ler sem vídeos: %#v", old)
	}

	garbage := attachmentSummaryFromPayload(map[string]any{
		"attachments": map[string]any{"videos": []any{
			map[string]any{"frames": 4.0},
			map[string]any{"frames": "oito"},
			map[string]any{"frames": 1.5},
			"clip.mp4",
		}},
	})
	if len(garbage.videos) != 1 || garbage.videos[0].frames != 4 {
		t.Fatalf("malformados deveriam ser ignorados: %#v", garbage.videos)
	}
}
