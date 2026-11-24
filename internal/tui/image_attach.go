package tui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
	"github.com/quebragalho-dev/quebragalho-cli/internal/media"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
	_ "golang.org/x/image/webp"
)

const attachmentThumbnailMaxPixels = 16 << 20

// tuiFFmpegGOOS/GOARCH decidem qual orientação de instalação do ffmpeg o aviso
// mostra. Vars para o teste fixar o sistema (vazio = plataforma do host, o
// único caminho da produção): a lista dos três sistemas confundia quem só
// precisa do seu, e mandava baixar no macOS, onde não há pacote fixado.
var (
	tuiFFmpegGOOS   = runtime.GOOS
	tuiFFmpegGOARCH = runtime.GOARCH
)

// droppableImageExts are the image extensions a dragged-and-dropped file may
// carry (matched case-insensitively); PDFs are recognized separately.
var droppableImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// droppedAttachmentPath recognizes a single drag-dropped (or pasted) file path
// that points at an existing image, video or PDF, returning the cleaned path. Terminals
// deliver a dropped file as its path with spaces/special chars backslash-escaped
// (or the whole path quoted); this undoes that so "Screenshot 2026 at 1.png"
// resolves. ok is false for anything that is not a single existing image/PDF
// file, so normal text pastes and real slash-commands are left untouched.
func droppedAttachmentPath(content, cwd string) (string, bool) {
	s := strings.TrimSpace(content)
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return "", false // empty or multi-line: not a single dropped file
	}
	// Browsers and some terminals paste a dropped file as a file:// URI
	// (percent-escaped). Decode it to a local path before the checks below;
	// anything that is not a file URI keeps the plain-path flow.
	if len(s) >= 7 && strings.EqualFold(s[:7], "file://") {
		decoded, ok := imageinput.ClipboardFileURLToPath(s)
		if !ok {
			return "", false
		}
		s = decoded
	}
	if unq, quoted := stripMatchingQuotes(s); quoted {
		s = unq // a quoted path is literal — do not unescape inside it
	} else {
		s = unescapeDroppedPath(s)
	}
	if s == "" {
		return "", false
	}
	resolved := s
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cwd, resolved)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	ext := strings.ToLower(filepath.Ext(s))
	if droppableImageExts[ext] || media.IsVideoPath(s) || ext == ".pdf" || imageinput.LooksLikeDocumentFile(s, cwd) {
		return s, true
	}
	return "", false
}

// unescapeDroppedPath drops a backslash before any following byte, undoing the
// terminal's drag-drop escaping ("\ " -> " ", "\(" -> "(", "\\" -> "\").
func unescapeDroppedPath(s string) string {
	if runtime.GOOS == "windows" {
		// On Windows the backslash is the path separator, not a drag-drop escape;
		// stripping it would corrupt real paths (C:\Users\… -> C:Users…). Dropped
		// paths there arrive quoted (handled by stripMatchingQuotes) or plain.
		return s
	}
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// stripMatchingQuotes removes a single pair of surrounding ' or " quotes.
func stripMatchingQuotes(s string) (string, bool) {
	if len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'') && s[len(s)-1] == q {
			return s[1 : len(s)-1], true
		}
	}
	return s, false
}

// modelSupportsVisionTUI reports whether the active model can accept image
// input. It checks three sources in order:
//  1. The curated model registry (catalog authority + name heuristic)
//  2. The discovered model list (if the live model picker fetched it) — this
//     carries InputModalities when the endpoint publishes them, including
//     "image" for vision-capable models
//  3. Falls back to the name heuristic for unknown models
func (m model) modelSupportsVisionTUI() bool {
	trimmed := strings.TrimSpace(m.modelName)
	if trimmed == "" {
		return false
	}
	// The curated catalog is authoritative only when it knows the model.
	if entry, known := m.modelCatalog.Resolve(trimmed); known {
		return entry.Supports(modelregistry.ModelCapabilityVision)
	}
	// Check the discovered model list for InputModalities containing "image".
	// This covers models not in the curated catalog — the live listing can
	// carry their capabilities.
	for _, models := range m.modelPickerLiveByProvider {
		for _, dm := range models {
			if strings.EqualFold(strings.TrimSpace(dm.ID), trimmed) {
				// A provider's authenticated listing may only establish which models
				// are available, without repeating modalities. Treat an empty list as
				// unknown rather than as an explicit image-input denial, so the
				// curated registry/name capability fallback remains available while
				// the endpoint's modality metadata is unavailable.
				if len(dm.InputModalities) == 0 {
					continue
				}
				for _, modality := range dm.InputModalities {
					if strings.EqualFold(strings.TrimSpace(modality), "image") {
						return true
					}
				}
				return false // found the model in discovered list, no image modality
			}
		}
	}
	// Fall back to the name heuristic for models not in the catalog or
	// discovered list.
	return modelregistry.VisionCapableByName(trimmed)
}

// attachClipboardImage attaches an image read from the OS clipboard (a
// screenshot paste). Runs through the same vision gate + size cap as
// /image <path>, but the bytes come from the clipboard instead of a file.
// The bytes are also staged in a private temp file (deleted when the
// attachment is discarded, consumed, or qg quits) so a pasted screenshot never
// sits in the shared system temp dir.
func (m model) attachClipboardImage(data []byte, mediaType string) model {
	if !m.modelSupportsVisionTUI() {
		name := displayValue(m.modelName, "o modelo atual")
		return m.appendImageNotice("O modelo " + name + " não suporta imagens; imagem da área de transferência recusada.")
	}
	if len(data) > imageinput.MaxImageBytes {
		return m.appendImageNotice("A imagem da área de transferência passa do limite de 10 MiB.")
	}
	tempPath := ""
	if p, err := saveClipboardTempFile(data, clipboardMediaTypeExt(mediaType)); err == nil {
		tempPath = p
	}
	m.appendPendingImage(qgruntime.ImageBlock{MediaType: mediaType, Data: data}, "clipboard", tempPath, "")
	m.refreshPendingImageThumbnail()
	return m
}

// clipboardMediaTypeExt maps a sniffed clipboard media type to a temp-file
// extension. Unknown types fall back to .png; the bytes were already sniffed,
// so this only names the staging file.
func clipboardMediaTypeExt(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// appendPendingImage stages one image with its display label, keeping
// pendingImageTemps and pendingImageOrigins in lockstep ("" when the image came
// from a real file and owns no temp; "" origin for clipboard pastes and video
// frames). Every image-staging site must go through here so the temp-file
// invariant holds and discard can clean up exactly what qg created.
func (m *model) appendPendingImage(block qgruntime.ImageBlock, label, tempPath, origin string) {
	m.pendingImages = append(m.pendingImages, block)
	m.pendingImageLabels = append(m.pendingImageLabels, label)
	m.pendingImageTemps = append(m.pendingImageTemps, tempPath)
	m.pendingImageOrigins = append(m.pendingImageOrigins, origin)
}

// attachmentOriginPath turns the path an attachment was loaded from into the
// absolute path that is kept for opening it later. Relative paths resolve
// against cwd, the same base the loaders use, so the stored path is always
// absolute and never starts with an option-like "-" when it reaches the viewer.
func attachmentOriginPath(path, cwd string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd != "" {
		return filepath.Join(cwd, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// discardPendingImageTemps deletes the private temp files backing staged
// clipboard images and forgets them. Call it wherever pendingImages is
// cleared or an entry is popped; paths qg did not create ("") are skipped.
// Best-effort: a removal failure leaves a stray file for quit-time cleanup,
// never an error in the user's face.
func (m *model) discardPendingImageTemps() {
	for _, p := range m.pendingImageTemps {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	m.pendingImageTemps = nil
}

// attachClipboardFiles attaches files copied in the OS clipboard (a
// Finder/Explorer/file-manager copy) the same way a drag-drop does: images,
// PDFs, vídeos e GIFs animados viram anexos com as prévias que já existem;
// qualquer outra coisa é ignorada. All user-facing text is in Portuguese.
func (m model) attachClipboardFiles(paths []string) (model, tea.Cmd) {
	attached := 0
	videoFailed := false
	var cmds []tea.Cmd
	for _, p := range paths {
		if media.IsVideoPath(p) || media.IsAnimatedGIFFile(p, m.cwd) {
			before := len(m.pendingImages)
			var cmd tea.Cmd
			m, cmd = m.handleVideoAttach(p)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if len(m.pendingImages) > before {
				attached++
			} else {
				videoFailed = true
			}
			continue
		}
		before := len(m.pendingImages) + len(m.pendingDocuments)
		if _, ok := droppedAttachmentPath(p, m.cwd); !ok {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.handleImageCommand(p)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if len(m.pendingImages)+len(m.pendingDocuments) > before {
			attached++
		}
	}
	switch {
	case attached > 0:
		// chips ([Image #N]/[Doc #N]) confirmam, como no arrastar
	case videoFailed:
		// handleVideoAttach já explicou o aviso em português
	default:
		m = m.appendImageNotice("Nenhum arquivo copiado pôde ser anexado — o qg anexa imagens, PDFs e vídeos.")
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// handleImageCommand processes "/image <path>" and "/image clear". A bare
// "/image" prints usage. PDFs are routed to the document path (text layer always
// attaches; pages rasterize to images only for vision models with a rasterizer).
// Image files attach only to vision models. Attachment failures (missing file,
// unsupported type, oversize) surface as an inline notice and attach nothing.
func (m model) handleImageCommand(arg string) (model, tea.Cmd) {
	trimmed := strings.TrimSpace(arg)
	switch {
	case trimmed == "":
		return m.appendImageNotice("Usage: /image <path>  (image or PDF; or /image clear)"), nil
	case strings.EqualFold(trimmed, "clear"):
		m.discardPendingImageTemps()
		m.cancelPendingVideoExtractions()
		m.pendingImages = nil
		m.pendingImageLabels = nil
		m.pendingImageOrigins = nil
		m.pendingImageThumbnails = nil
		m.pendingDocuments = nil
		m.pendingVideoInfos = nil
		return m.appendImageNotice("Cleared pending attachments."), nil
	}

	// Vídeo e GIF animado viram quadros (handleVideoAttach, isolado para
	// facilitar junção com a colagem da área de transferência); GIF de um
	// quadro só cai no caminho de imagem abaixo.
	if media.IsVideoPath(trimmed) || media.IsAnimatedGIFFile(trimmed, m.cwd) {
		return m.handleVideoAttach(trimmed)
	}

	// A PDF carries a text layer every model can read, so it is not gated on
	// vision the way a raw image is; the optional rasterized pages are. Route by
	// the ".pdf" hint OR a content sniff so a real PDF whose name lacks the
	// extension still reaches the document path rather than the vision-only image
	// path. The cheap header sniff runs before the vision gate.
	if imageinput.IsProbablyDocumentPath(trimmed) || imageinput.LooksLikeDocumentFile(trimmed, m.cwd) {
		return m.handleDocumentAttach(trimmed), nil
	}

	if !m.modelSupportsVisionTUI() {
		name := displayValue(m.modelName, "o modelo atual")
		return m.appendImageNotice("O modelo " + name + " não suporta imagens; anexo recusado."), nil
	}

	block, err := imageinput.LoadFile(trimmed, m.cwd)
	if err != nil {
		return m.appendImageNotice(err.Error()), nil
	}

	m.appendPendingImage(block, filepath.Base(trimmed), "", attachmentOriginPath(trimmed, m.cwd))
	m.refreshPendingImageThumbnail()
	// No "attached" system message: the composer attachment chip ([Image #N]) is
	// the confirmation, matching the compact attach UX.
	return m, nil
}

// pendingDocument is a PDF staged by /image for the next user turn: its extracted
// text layer (prepended to the prompt at submit time) and a display label.
type pendingDocument struct {
	label string
	text  string
	// path is the absolute path of the PDF the text came from; a click on its
	// chip opens that original file.
	path string
}

// handleDocumentAttach loads a PDF through imageinput.LoadDocument. The text
// layer is staged for every model; when the active model supports vision and a
// rasterizer is available, the rendered pages are staged through the existing
// pending-image pipeline too. A scanned PDF with no text (and no rasterizer)
// surfaces LoadDocument's explicit "no extractable text" notice and attaches
// nothing.
func (m model) handleDocumentAttach(path string) model {
	doc, err := imageinput.LoadDocument(path, m.cwd, imageinput.DocumentOptions{
		Vision: m.modelSupportsVisionTUI(),
	})
	if err != nil {
		return m.appendImageNotice(err.Error())
	}

	label := filepath.Base(path)
	origin := attachmentOriginPath(path, m.cwd)
	if strings.TrimSpace(doc.Text) != "" {
		m.pendingDocuments = append(m.pendingDocuments, pendingDocument{label: label, text: doc.Text, path: origin})
	}
	for _, block := range doc.Images {
		m.appendPendingImage(block, label, "", origin)
	}
	m.refreshPendingImageThumbnail()
	// The composer attachment chip ([Doc #N] / [Image #N]) is the confirmation; no
	// "attached" system message.
	return m
}

// blockWhileExtracting stops a new turn while frames extract, keeping the
// draft and queue untouched and telling the user to wait for the frames.
func (m model) blockWhileExtracting() (model, bool) {
	if _, ext, ok := m.newestPendingVideoExtraction(); ok {
		return m.appendSystemNotice(extractWaitNotice(ext.label)), true
	}
	return m, false
}

func extractWaitNotice(label string) string {
	return "Aguarde: extraindo quadros de " + label + "…"
}

// pendingVideoInfo is a staged video attachment: its frames ride pendingImages
// like ordinary images, and text names the video plus when each frame was
// taken so the model can attribute them.
// pendingVideoExtraction is an in-flight background frame extraction.
type pendingVideoExtraction struct {
	label  string
	cancel context.CancelFunc
}

// newestPendingVideoExtraction returns the most recently started in-flight
// extraction, if any.
func (m model) newestPendingVideoExtraction() (uint64, pendingVideoExtraction, bool) {
	var best uint64
	var bestExt pendingVideoExtraction
	found := false
	for id, ext := range m.pendingVideoExtractions {
		if !found || id > best {
			best, bestExt, found = id, ext, true
		}
	}
	return best, bestExt, found
}

// cancelPendingVideoExtractions drops every in-flight frame extraction and
// stops its ffmpeg. Call it wherever staged attachments are discarded, so
// late frames never land in the wrong turn.
func (m *model) cancelPendingVideoExtractions() {
	for id, ext := range m.pendingVideoExtractions {
		ext.cancel()
		delete(m.pendingVideoExtractions, id)
	}
}

type pendingVideoInfo struct {
	label  string
	text   string
	frames int
	// gif diz se os quadros vieram de um GIF animado ([GIF #N]) ou de um
	// contêiner de vídeo ([Vídeo #N]).
	gif bool
	// source é o caminho absoluto do vídeo (ou GIF) original; um clique no chip
	// abre esse arquivo, nunca os quadros.
	source string
}

// videoFramesMsg carries a finished background frame extraction: on success
// the frames stage as image attachments with their context line.
type videoFramesMsg struct {
	id       uint64
	label    string
	source   string
	frames   []media.Frame
	duration float64
	err      error
}

// extractVideoFramesCmd runs the ffmpeg extraction off the TUI thread: even a
// short clip blocks the UI for seconds, and a long one for over ten.
func extractVideoFramesCmd(ctx context.Context, path, cwd, ffmpegPath, ffprobePath string, id uint64) tea.Cmd {
	return func() tea.Msg {
		tctx, cancel := context.WithTimeout(ctx, media.VideoAttachTimeout)
		defer cancel()
		frames, duration, err := media.ExtractAttachmentFrames(tctx, ffmpegPath, ffprobePath, path, cwd, media.ExtractOptions{})
		return videoFramesMsg{id: id, label: filepath.Base(path), source: attachmentOriginPath(path, cwd), frames: frames, duration: duration, err: err}
	}
}

// handleVideoAttach stages a video file (or animated GIF) as image frames for
// the next user turn, with a context line (duration, frame count, instants).
// Single-frame GIFs never reach here: they follow the plain image path.
// Videos extract in the background (a ffmpeg pass takes seconds); GIFs decode
// synchronously via stdlib. Failure (no vision, no ffmpeg, oversize,
// undecodable) surfaces an inline notice in Portuguese and attaches nothing.
func (m model) handleVideoAttach(path string) (model, tea.Cmd) {
	if !m.modelSupportsVisionTUI() {
		return m.appendImageNotice("O modelo " + displayValue(m.modelName, "o modelo atual") + " não suporta imagens; anexo de vídeo recusado."), nil
	}
	label := filepath.Base(path)
	if media.IsVideoPath(path) {
		ffmpegPath, ffprobePath, ok := media.ResolveFFmpeg()
		if !ok {
			return m.appendImageNotice("ffmpeg não encontrado para extrair os quadros do vídeo. " + media.FFmpegMissingHint(tuiFFmpegGOOS, tuiFFmpegGOARCH) + "."), nil
		}
		m = m.showTransientNoticeInline("Extraindo quadros de "+label+"…", transientNoticeInfo)
		m.videoExtractSeq++
		id := m.videoExtractSeq
		ctx, cancel := context.WithCancel(context.Background())
		if m.pendingVideoExtractions == nil {
			m.pendingVideoExtractions = map[uint64]pendingVideoExtraction{}
		}
		m.pendingVideoExtractions[id] = pendingVideoExtraction{label: label, cancel: cancel}
		return m, extractVideoFramesCmd(ctx, path, m.cwd, ffmpegPath, ffprobePath, id)
	}
	frames, duration, err := media.ExtractAttachmentFrames(context.Background(), "", "", path, m.cwd, media.ExtractOptions{})
	if err != nil {
		return m.appendImageNotice(err.Error()), nil
	}
	return m.stageVideoFrames(label, attachmentOriginPath(path, m.cwd), frames, duration, true), nil
}

// stageVideoFrames stages extracted frames as pending images with their
// context line, refreshing the first-frame preview. animatedGIF marks frames
// decoded from an animated GIF (a [GIF #N] chip) rather than a video
// container (a [Vídeo #N] chip); the staged bytes are identical either way.
func (m model) stageVideoFrames(label, source string, frames []media.Frame, duration float64, animatedGIF bool) model {
	// Frames carry no origin of their own: a click on one opens the video's
	// original through its chip (pendingVideoInfo.source), never the frame.
	for _, frame := range frames {
		m.appendPendingImage(qgruntime.ImageBlock{MediaType: "image/jpeg", Data: frame.Data}, label, "", "")
	}
	m.pendingVideoInfos = append(m.pendingVideoInfos, pendingVideoInfo{
		label:  label,
		text:   media.SummarizeFrames(label, duration, frames),
		frames: len(frames),
		gif:    animatedGIF,
		source: source,
	})
	m.refreshPendingImageThumbnail()
	return m
}

// consumePendingVideoPreamble returns the staged video context lines for the
// prompt preamble, or "" when the active model cannot receive them (the frames
// are dropped with a notice by the submit path, so the context line must not
// go alone). It always clears the pending video infos.
func (m *model) consumePendingVideoPreamble() string {
	if !m.modelSupportsVisionTUI() {
		m.pendingVideoInfos = nil
		return ""
	}
	return m.consumePendingVideos()
}

// consumePendingVideos returns the staged video context lines formatted as a
// prompt preamble and clears the pending video infos. Empty when nothing was
// staged; videos whose frames were all removed are skipped.
func (m *model) consumePendingVideos() string {
	if len(m.pendingVideoInfos) == 0 {
		return ""
	}
	var b strings.Builder
	for _, info := range m.pendingVideoInfos {
		if info.frames <= 0 || strings.TrimSpace(info.text) == "" {
			continue
		}
		b.WriteString(info.text)
		b.WriteString("\n")
	}
	m.pendingVideoInfos = nil
	return b.String()
}

// consumePendingDocuments returns the staged document text formatted as a prompt
// preamble and clears the pending documents. The preamble names each document so
// the model can attribute the text; an empty result means nothing was staged.
func (m *model) consumePendingDocuments() string {
	if len(m.pendingDocuments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, doc := range m.pendingDocuments {
		b.WriteString("Attached document: ")
		b.WriteString(doc.label)
		b.WriteString("\n")
		b.WriteString(doc.text)
		b.WriteString("\n\n")
	}
	m.pendingDocuments = nil
	return b.String()
}

// appendImageNotice appends an image-related notice to the transcript. Image
// errors (vision gate refusal, oversize, unsupported type) render with a red
// error border + red text so they stand out from ordinary grey system notes.
func (m model) appendImageNotice(text string) model {
	row := transcriptRow{
		kind: rowError,
		text: text,
	}
	m.transcript = appendTranscriptRow(m.transcript, row)
	return m
}

// removeLastAttachment drops the rightmost pending attachment chip and reports
// whether anything was removed. Documents render after images, so a staged
// document is removed before images; image pops keep pendingImages and
// pendingImageLabels in lockstep.
func (m model) removeLastAttachment() (model, bool) {
	if n := len(m.pendingDocuments); n > 0 {
		m.pendingDocuments = m.pendingDocuments[:n-1]
		return m, true
	}
	if n := len(m.pendingVideoInfos); n > 0 {
		info := m.pendingVideoInfos[n-1]
		removed := 0
		for removed < info.frames && len(m.pendingImageLabels) > 0 && m.pendingImageLabels[len(m.pendingImageLabels)-1] == info.label {
			m.pendingImageLabels = m.pendingImageLabels[:len(m.pendingImageLabels)-1]
			if len(m.pendingImages) > 0 {
				m.pendingImages = m.pendingImages[:len(m.pendingImages)-1]
			}
			if len(m.pendingImageTemps) > 0 {
				m.pendingImageTemps = m.pendingImageTemps[:len(m.pendingImageTemps)-1]
			}
			if len(m.pendingImageOrigins) > 0 {
				m.pendingImageOrigins = m.pendingImageOrigins[:len(m.pendingImageOrigins)-1]
			}
			removed++
		}
		if removed > 0 {
			m.pendingVideoInfos = m.pendingVideoInfos[:n-1]
			m.refreshPendingImageThumbnail()
			return m, true
		}
		// A cauda não é deste vídeo (ex. imagem anexada depois): cai no
		// caminho de imagem avulsa abaixo.
	}
	if n := len(m.pendingImageLabels); n > 0 {
		// A clipboard-pasted image owns a private temp file: delete exactly
		// what qg created, never pre-existing user files ("" = real file).
		if len(m.pendingImageTemps) == len(m.pendingImageLabels) && m.pendingImageTemps[n-1] != "" {
			_ = os.Remove(m.pendingImageTemps[n-1])
		}
		m.pendingImageLabels = m.pendingImageLabels[:n-1]
		if len(m.pendingImages) > 0 {
			m.pendingImages = m.pendingImages[:len(m.pendingImages)-1]
		}
		if len(m.pendingImageTemps) > 0 {
			m.pendingImageTemps = m.pendingImageTemps[:len(m.pendingImageTemps)-1]
		}
		if len(m.pendingImageOrigins) > 0 {
			m.pendingImageOrigins = m.pendingImageOrigins[:len(m.pendingImageOrigins)-1]
		}
		m.refreshPendingImageThumbnail()
		return m, true
	}
	// Nada anexado, mas há em andamento: Backspace cancela a mais
	// recente em vez de deixar os quadros caírem no próximo turno.
	if id, ext, ok := m.newestPendingVideoExtraction(); ok {
		ext.cancel()
		delete(m.pendingVideoExtractions, id)
		m = m.showTransientNoticeInline("Extração de vídeo cancelada: "+ext.label, transientNoticeInfo)
		return m, true
	}
	return m, false
}

// refreshPendingImageThumbnail prepares only the first pending image for the
// optional inline terminal preview. Decode failures deliberately leave the
// attachment intact: the text chip remains the portable fallback and the model
// still receives the original bytes.
func (m *model) refreshPendingImageThumbnail() {
	m.pendingImageThumbnails = nil
	if len(m.pendingImages) == 0 {
		return
	}
	count := min(len(m.pendingImages), attachmentPreviewMaxImages)
	m.pendingImageThumbnails = make([]*termimage.Animation, count)
	for index := 0; index < count; index++ {
		preview, err := attachmentThumbnail(m.pendingImages[index])
		if err == nil {
			m.pendingImageThumbnails[index] = preview
		}
	}
}

func attachmentThumbnail(block qgruntime.ImageBlock) (*termimage.Animation, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(block.Data))
	if err != nil {
		return nil, err
	}
	if config.Width < 1 || config.Height < 1 || config.Width > attachmentThumbnailMaxPixels/config.Height {
		return nil, fmt.Errorf("image is too large for an inline thumbnail")
	}
	decoded, _, err := image.Decode(bytes.NewReader(block.Data))
	if err != nil {
		return nil, err
	}
	return termimage.ThumbnailAnimation(decoded)
}

// renderAttachmentChips builds the pending-attachment row from both staged images
// and staged documents, e.g. "[Image #1] [Image #2] [Doc #1]". Returns "" when
// nothing is staged. Numbered (not named) so a long screenshot path never shows
// in the composer.
// visionDropWarning returns a one-line notice when images are staged but the
// (now active) model can't accept them, so switching to a non-vision model warns
// the user immediately at switch time instead of silently dropping the images at
// submit. Empty when there is nothing staged or the model supports vision.
func (m model) visionDropWarning() string {
	if len(m.pendingImages) == 0 || m.modelSupportsVisionTUI() {
		return ""
	}
	return fmt.Sprintf("⚠ %d imagem(ns) na fila serão descartadas — %s não tem suporte a visão.",
		len(m.pendingImages), displayValue(m.modelName, "o modelo atual"))
}

func renderAttachmentChips(imageLabels []string, docs []pendingDocument, videos []pendingVideoInfo) string {
	chips := attachmentChipList(imageLabels, docs, videos)
	texts := make([]string, len(chips))
	for i, chip := range chips {
		texts[i] = chip.text
	}
	return strings.Join(texts, " ")
}

// attachmentChipKind says which kind of staged attachment a composer chip names.
type attachmentChipKind int

const (
	attachmentChipImage attachmentChipKind = iota
	attachmentChipVideo
	attachmentChipDocument
)

// attachmentChip is one composer chip, with the staged entries it stands for so
// a click on it (or on a thumbnail of one of its images) can find the attachment.
type attachmentChip struct {
	text string
	kind attachmentChipKind
	// index addresses pendingImages (image), pendingVideoInfos (video) or
	// pendingDocuments (document), depending on kind.
	index int
	// first and images give the pendingImages range the chip covers: one entry
	// for an image, the frame count for a video, none for a document.
	first  int
	images int
}

// attachmentChipList builds the composer chips in display order, with the same
// numbering and text renderAttachmentChips always produced.
func attachmentChipList(imageLabels []string, docs []pendingDocument, videos []pendingVideoInfo) []attachmentChip {
	chips := make([]attachmentChip, 0, len(imageLabels)+len(docs)+len(videos))
	imageNum, videoNum, gifNum := 0, 0, 0
	nextVideo := 0
	index := 0
	for index < len(imageLabels) {
		// Os quadros de um vídeo entram em pendingImages contíguos na ordem
		// em que cada extração concluiu — a mesma ordem de pendingVideoInfos.
		// Quando a sequência casa, os K quadros viram um só chip; senão a
		// entrada conta como imagem avulsa para nenhum chip se perder.
		if nextVideo < len(videos) && videos[nextVideo].frames > 0 &&
			hasVideoRun(imageLabels, index, videos[nextVideo]) {
			info := videos[nextVideo]
			chip := attachmentChip{kind: attachmentChipVideo, index: nextVideo, first: index, images: info.frames}
			if info.gif {
				gifNum++
				chip.text = fmt.Sprintf("[GIF #%d · %s]", gifNum, quadroLabel(info.frames))
			} else {
				videoNum++
				chip.text = fmt.Sprintf("[Vídeo #%d · %s]", videoNum, quadroLabel(info.frames))
			}
			chips = append(chips, chip)
			index += info.frames
			nextVideo++
			continue
		}
		imageNum++
		chips = append(chips, attachmentChip{
			text: fmt.Sprintf("[Image #%d]", imageNum), kind: attachmentChipImage,
			index: index, first: index, images: 1,
		})
		index++
	}
	for i := range docs {
		chips = append(chips, attachmentChip{text: fmt.Sprintf("[Doc #%d]", i+1), kind: attachmentChipDocument, index: i})
	}
	return chips
}

// hasVideoRun diz se imageLabels tem, a partir de start, os frames de info
// (rótulos contíguos iguais ao do vídeo, na quantidade extraída).
func hasVideoRun(imageLabels []string, start int, info pendingVideoInfo) bool {
	if info.frames <= 0 || start+info.frames > len(imageLabels) {
		return false
	}
	for _, label := range imageLabels[start : start+info.frames] {
		if label != info.label {
			return false
		}
	}
	return true
}

// quadroLabel devolve "1 quadro" ou "N quadros" para o chip de vídeo/GIF.
func quadroLabel(frames int) string {
	if frames == 1 {
		return "1 quadro"
	}
	return fmt.Sprintf("%d quadros", frames)
}

// pendingVideoFrameCount soma os quadros com extração concluída.
func pendingVideoFrameCount(videos []pendingVideoInfo) int {
	total := 0
	for _, info := range videos {
		if info.frames > 0 {
			total += info.frames
		}
	}
	return total
}

// pendingVideoSummaries converte os vídeos com quadros para o resumo do
// transcript/sessão, na ordem de anexo; entradas vazias não aparecem.
func pendingVideoSummaries(videos []pendingVideoInfo) []videoAttachmentSummary {
	var out []videoAttachmentSummary
	for _, info := range videos {
		if info.frames <= 0 {
			continue
		}
		out = append(out, videoAttachmentSummary{frames: info.frames, gif: info.gif})
	}
	return out
}
