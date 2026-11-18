package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limites padrão da extração: até 8 quadros por vídeo, lado maior de 1024 px,
// vídeos de no máximo 512 MiB e 30 min. Arquivos acima disso são recusados
// com mensagem em português antes de qualquer processamento pesado.
const (
	DefaultMaxFrames   = 8
	DefaultMaxSide     = 1024
	DefaultJPEGQuality = 3
	DefaultMaxBytes    = 512 << 20
	DefaultMaxDuration = 30 * 60.0
)

// VideoInfo descreve o que o probe encontrou no arquivo.
type VideoInfo struct {
	DurationSec float64
	Width       int
	Height      int
}

// Frame é um quadro extraído, pronto para anexar como imagem (JPEG).
type Frame struct {
	Data         []byte
	TimestampSec float64
}

// ExtractOptions configura a extração de quadros.
type ExtractOptions struct {
	MaxFrames   int
	MaxSide     int
	JPEGQuality int
	MaxBytes    int64
	MaxDuration float64
}

func (o ExtractOptions) withDefaults() ExtractOptions {
	if o.MaxFrames <= 0 {
		o.MaxFrames = DefaultMaxFrames
	}
	if o.MaxSide <= 0 {
		o.MaxSide = DefaultMaxSide
	}
	if o.JPEGQuality <= 0 {
		o.JPEGQuality = DefaultJPEGQuality
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = DefaultMaxDuration
	}
	return o
}

func checkVideoSize(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("não foi possível ler o vídeo %s: %w", filepath.Base(path), err)
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("vídeo %s grande demais (%s, limite %s) para extrair quadros", filepath.Base(path), formatBytes(info.Size()), formatBytes(maxBytes))
	}
	return nil
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// Probe lê duração e dimensões do vídeo: ffprobe (JSON) quando disponível,
// senão a saída de "ffmpeg -i" (stderr). Sem shell: argv diretos, então
// caminhos com espaço ou acento passam intactos no Windows.
func Probe(ctx context.Context, ffmpegPath, ffprobePath, videoPath string) (VideoInfo, error) {
	if strings.TrimSpace(ffprobePath) != "" {
		info, err := probeFFprobe(ctx, ffprobePath, videoPath)
		if err == nil {
			return info, nil
		}
	}
	return probeFFmpegFallback(ctx, ffmpegPath, videoPath)
}

type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Duration string `json:"duration"`
	} `json:"streams"`
}

func probeFFprobe(ctx context.Context, ffprobePath, videoPath string) (VideoInfo, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,duration",
		"-show_entries", "format=duration",
		"-of", "json",
		videoPath,
	)
	// Se o processo morrer e um neto segurar os pipes, não esperar além disto.
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return VideoInfo{}, fmt.Errorf("leitura do vídeo %s: tempo esgotado ou cancelado", filepath.Base(videoPath))
		}
		return VideoInfo{}, compactExecError("ler a duração do vídeo", err)
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return VideoInfo{}, fmt.Errorf("não foi possível ler a duração do vídeo: saída do ffprobe inválida")
	}
	duration := firstPositiveDuration(parsed.Format.Duration)
	if len(parsed.Streams) > 0 {
		if duration <= 0 {
			duration = firstPositiveDuration(parsed.Streams[0].Duration)
		}
		if parsed.Streams[0].Width > 0 && parsed.Streams[0].Height > 0 {
			return VideoInfo{DurationSec: duration, Width: parsed.Streams[0].Width, Height: parsed.Streams[0].Height}, nil
		}
	}
	if duration > 0 {
		return VideoInfo{DurationSec: duration}, nil
	}
	return VideoInfo{}, fmt.Errorf("não foi possível ler a duração do vídeo %s", filepath.Base(videoPath))
}

func firstPositiveDuration(text string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

var (
	ffmpegDurationPattern = regexp.MustCompile(`Duration: (\d+):(\d+):([\d.]+)`)
	ffmpegDimsPattern     = regexp.MustCompile(`Video: [^\n]*?, (\d+)x(\d+)`)
)

func probeFFmpegFallback(ctx context.Context, ffmpegPath, videoPath string) (VideoInfo, error) {
	if strings.TrimSpace(ffmpegPath) == "" {
		return VideoInfo{}, fmt.Errorf("ffmpeg não encontrado para ler o vídeo %s", filepath.Base(videoPath))
	}
	// "ffmpeg -i arquivo" sem saída falha de propósito e despeja os
	// metadados no stderr: é daí que saem duração e dimensões.
	cmd := exec.CommandContext(ctx, ffmpegPath, "-i", videoPath)
	cmd.WaitDelay = 10 * time.Second
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if ctx.Err() != nil {
		return VideoInfo{}, fmt.Errorf("leitura do vídeo %s: tempo esgotado ou cancelado", filepath.Base(videoPath))
	}
	text := stderr.String()
	duration := 0.0
	if match := ffmpegDurationPattern.FindStringSubmatch(text); match != nil {
		hours, _ := strconv.ParseFloat(match[1], 64)
		minutes, _ := strconv.ParseFloat(match[2], 64)
		seconds, _ := strconv.ParseFloat(match[3], 64)
		duration = hours*3600 + minutes*60 + seconds
	}
	width, height := 0, 0
	if match := ffmpegDimsPattern.FindStringSubmatch(text); match != nil {
		width, _ = strconv.Atoi(match[1])
		height, _ = strconv.Atoi(match[2])
	}
	if duration <= 0 && (width <= 0 || height <= 0) {
		return VideoInfo{}, fmt.Errorf("não foi possível ler o vídeo %s (formato não reconhecido?)", filepath.Base(videoPath))
	}
	return VideoInfo{DurationSec: duration, Width: width, Height: height}, nil
}

// compactExecError traduz falha de processo (incluindo timeout do contexto)
// em erro curto em português.
func compactExecError(what string, err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			firstLine, _, _ := strings.Cut(stderr, "\n")
			return fmt.Errorf("%s: %s", what, firstLine)
		}
		return fmt.Errorf("%s: processo saiu com erro", what)
	}
	return fmt.Errorf("%s: tempo esgotado ou cancelado (%v)", what, err)
}

// VideoAttachTimeout limita probe+extração no anexo interativo.
const VideoAttachTimeout = 3 * time.Minute

// ExtractAttachmentFrames extrai quadros JPEG espaçados de um anexo de vídeo
// (via ffmpeg) ou GIF animado (stdlib, sem ffmpeg). Devolve os quadros e a
// duração em segundos. GIF de 1 quadro não é vídeo: use o caminho de imagem.
func ExtractAttachmentFrames(ctx context.Context, ffmpegPath, ffprobePath, path, workspaceRoot string, opts ExtractOptions) ([]Frame, float64, error) {
	resolved := strings.TrimSpace(path)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspaceRoot, resolved)
	}
	if IsVideoPath(resolved) {
		return ExtractFrames(ctx, ffmpegPath, ffprobePath, resolved, opts)
	}
	data, err := readCappedFile(resolved, maxGIFBytes+1)
	if err != nil {
		return nil, 0, err
	}
	animated, err := IsAnimatedGIF(bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	if !animated {
		return nil, 0, fmt.Errorf("GIF de 1 quadro: anexe como imagem normal")
	}
	return ExtractGIFFrames(data, opts.MaxFrames, opts.MaxSide)
}

func readCappedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("não foi possível ler %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, fmt.Errorf("não foi possível ler %s: %w", filepath.Base(path), err)
	}
	return data, nil
}

// SummarizeFrames monta a linha de contexto enviada junto aos quadros:
// nome, duração, quantidade e instantes.
func SummarizeFrames(label string, durationSec float64, frames []Frame) string {
	instants := make([]string, 0, len(frames))
	for _, frame := range frames {
		instants = append(instants, "t="+formatSeconds(frame.TimestampSec))
	}
	return fmt.Sprintf("Attached video: %s (%ss, %d frames at %s)", label, formatSeconds(durationSec), len(frames), strings.Join(instants, ", "))
}

func formatSeconds(value float64) string {
	return strconv.FormatFloat(value, 'f', 1, 64) + "s"
}

// ExtractFrames extrai até MaxFrames quadros JPEG espaçados pela duração,
// escalados para caber em MaxSide no lado maior. O contexto limita e cancela
// tudo (o ffmpeg morre junto via CommandContext).
func ExtractFrames(ctx context.Context, ffmpegPath, ffprobePath, videoPath string, opts ExtractOptions) ([]Frame, float64, error) {
	opts = opts.withDefaults()
	if err := checkVideoSize(videoPath, opts.MaxBytes); err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("extração de quadros cancelada antes de começar: tempo esgotado ou cancelado")
	}
	info, err := Probe(ctx, ffmpegPath, ffprobePath, videoPath)
	if err != nil {
		return nil, 0, err
	}
	if info.DurationSec > opts.MaxDuration {
		return nil, 0, fmt.Errorf("vídeo %s longo demais (%.0f s, limite %.0f s) para extrair quadros", filepath.Base(videoPath), info.DurationSec, opts.MaxDuration)
	}
	frames := opts.MaxFrames
	fps := 0.0
	if info.DurationSec > 0 {
		fps = float64(frames) / info.DurationSec
	}
	if fps <= 0 {
		frames = 1
		fps = 1
	}

	outDir, err := os.MkdirTemp("", "qg-video-frames-*")
	if err != nil {
		return nil, 0, fmt.Errorf("preparar extração de quadros: %w", err)
	}
	defer func() { _ = os.RemoveAll(outDir) }()
	pattern := filepath.Join(outDir, "frame%03d.jpg")
	filter := fmt.Sprintf("fps=%s,scale='min(iw,%d)':-2,scale=-2:'min(ih,%d)'",
		strconv.FormatFloat(fps, 'f', 6, 64), opts.MaxSide, opts.MaxSide)

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-y",
		"-i", videoPath,
		"-vf", filter,
		"-frames:v", strconv.Itoa(frames),
		"-q:v", strconv.Itoa(opts.JPEGQuality),
		pattern,
	)
	cmd.WaitDelay = 10 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, fmt.Errorf("extração de quadros do vídeo %s: tempo esgotado ou cancelado", filepath.Base(videoPath))
		}
		firstLine, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if firstLine == "" {
			firstLine = err.Error()
		}
		return nil, 0, fmt.Errorf("extração de quadros do vídeo %s falhou: %s", filepath.Base(videoPath), firstLine)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, 0, fmt.Errorf("ler quadros extraídos: %w", err)
	}
	result := []Frame{}
	for index, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jpg") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(outDir, entry.Name()))
		if err != nil {
			return nil, 0, fmt.Errorf("ler quadro extraído: %w", err)
		}
		if len(data) == 0 {
			continue
		}
		timestamp := float64(index) / fps
		if timestamp > info.DurationSec && info.DurationSec > 0 {
			timestamp = info.DurationSec
		}
		result = append(result, Frame{Data: data, TimestampSec: timestamp})
	}
	if len(result) == 0 {
		return nil, 0, fmt.Errorf("ffmpeg não gerou quadros do vídeo %s", filepath.Base(videoPath))
	}
	return result, info.DurationSec, nil
}
