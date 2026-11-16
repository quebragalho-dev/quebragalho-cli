package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// sttDownloadProgressMsg carries a live download-status line (with a
// percentage) into the TUI, injected from the download goroutine via
// runtimeMessageSink.
type sttDownloadProgressMsg struct{ status string }

// sttDownloadProgressBytesMsg carries the live byte counts of the file being
// downloaded; the status line renders them as a progress bar.
type sttDownloadProgressBytesMsg struct {
	label string
	done  int64
	total int64
}

// dictationDownloadedMsg reports the outcome of an auto-download.
type dictationDownloadedMsg struct {
	components dictation.EngineComponents
	label      string // friendly model name for the ready notice ("" → generic)
	err        error
}

// canOfferDownload reports whether a failed build should point the user at the
// /stt-model download chooser: the failure is a missing-setup error, the user is
// on the local provider, this platform has a prebuilt engine, and a download
// root is configured.
func (d dictationController) canOfferDownload(err error) bool {
	var setupErr *dictation.SetupError
	return d.downloadRoot != "" && !d.downloading &&
		d.cfg.STTProvider() == config.STTProviderLocal &&
		dictation.AutoDownloadSupported() &&
		errors.As(err, &setupErr)
}

// startVariantDownload fetches the sherpa-onnx engine + the chosen model
// variant, reporting progress live, then persists and applies the resolved paths.
func (m model) startVariantDownload(v dictation.ModelVariant) (model, tea.Cmd) {
	if m.dictation.downloading {
		return m.showTransientNoticeInline("A configuração do ditado já está em andamento.", transientNoticeWarning), nil
	}
	m.dictation.downloading = true
	sink := m.runtimeMessageSink
	root := m.dictation.downloadRoot
	version := m.dictation.cfg.EngineVersion
	base := m.ctx
	if base == nil {
		base = context.Background()
	}
	m.dictation.downloadStatus = "Iniciando download…"
	m = m.showTransientNoticeInline(fmt.Sprintf("Configurando ditado local (%s) — o progresso está na linha de status.", v.Label), transientNoticeInfo)
	return m, func() tea.Msg {
		comp, err := dictation.EnsureLocalEngine(base, dictation.DownloadOptions{
			DestRoot:          root,
			EngineVersion:     version,
			ModelAssetName:    v.AssetName,
			ModelPinnedDigest: v.Digest,
			ModelDirName:      v.DirName,
			ModelLabel:        v.Label,
			Progress: func(status string) {
				if sink != nil {
					sink(sttDownloadProgressMsg{status: status})
				}
			},
			ProgressBytes: func(label string, done, total int64) {
				if sink != nil {
					sink(sttDownloadProgressBytesMsg{label: label, done: done, total: total})
				}
			},
		})
		return dictationDownloadedMsg{components: comp, label: v.Label, err: err}
	}
}

// handleDictationDownloadProgress updates the single live status line (shown in
// the status bar) — not a new transcript line per update.
func (m model) handleDictationDownloadProgress(msg sttDownloadProgressMsg) model {
	if m.dictation.downloading {
		m.dictation.downloadStatus = msg.status
	}
	return m
}

// handleDictationDownloadProgressBytes stores the live byte counts the status
// line renders as a progress bar. The engine reports itself as "Engine"; the
// bar shows "Motor".
func (m model) handleDictationDownloadProgressBytes(msg sttDownloadProgressBytesMsg) model {
	if m.dictation.downloading {
		label := msg.label
		if label == "Engine" {
			label = "Motor"
		}
		m.dictation.downloadLabel = label
		m.dictation.downloadDone = msg.done
		m.dictation.downloadTotal = msg.total
	}
	return m
}

// downloadBarCells is the fixed width of the download progress bar.
const downloadBarCells = 20

// renderDownloadBar draws the filled/empty cells for done/total bytes. filled
// is integer division, so the bar only reads full when done == total.
func renderDownloadBar(done, total int64) string {
	filled := 0
	if total > 0 && done > 0 {
		filled = int(done * downloadBarCells / total)
		if filled > downloadBarCells {
			filled = downloadBarCells
		}
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", downloadBarCells-filled)
}

// downloadProgressPercent rounds the completion to whole percent, clamped to
// 0..100.
func downloadProgressPercent(done, total int64) int64 {
	if total <= 0 || done <= 0 {
		return 0
	}
	pct := (done*100 + total/2) / total
	if pct > 100 {
		pct = 100
	}
	return pct
}

// downloadProgressText renders the byte counts as "<label> <bar> <pct>% ·
// <done>/<total> MB" (withBar) or without the bar for narrow terminals. The
// numbers, not the status string, are the source.
func downloadProgressText(label string, done, total int64, withBar bool) string {
	pct := downloadProgressPercent(done, total)
	if withBar {
		return fmt.Sprintf("%s %s %d%% · %d/%d MB", label, renderDownloadBar(done, total), pct, done>>20, total>>20)
	}
	return fmt.Sprintf("%s %d%% · %d/%d MB", label, pct, done>>20, total>>20)
}

// dictationDownloadBarText is the live download line for the status bar: the
// byte-count bar while the size is known, else the legacy text status. The
// bar drops first when the line does not fit width, and the result never
// wraps (the caller truncates).
func (m model) dictationDownloadBarText(width int, prefix, btwChip string) string {
	d := m.dictation
	if label := strings.TrimSpace(d.downloadLabel); label != "" && d.downloadTotal > 0 {
		if full := downloadProgressText(label, d.downloadDone, d.downloadTotal, true); lipgloss.Width(prefix+btwChip+"⬇ "+full) <= width {
			return full
		}
		return downloadProgressText(label, d.downloadDone, d.downloadTotal, false)
	}
	return strings.TrimSpace(d.downloadStatus)
}

// handleDictationDownloaded persists and applies the downloaded engine, or
// reports the failure.
func (m model) handleDictationDownloaded(msg dictationDownloadedMsg) (model, tea.Cmd) {
	m.dictation.downloading = false
	m.dictation.downloadStatus = ""
	m.dictation.downloadDone, m.dictation.downloadTotal = 0, 0
	m.dictation.downloadLabel = ""
	if msg.err != nil {
		m.dictation.voiceAfterSetup = false
		return m.appendSystemNotice("Falha na configuração do ditado: " + dictationErrorText(msg.err)), nil
	}
	m, err := m.applyEngineComponents(msg.components)
	if err != nil {
		m.dictation.voiceAfterSetup = false
		return m.appendSystemNotice("Motor baixado, mas não foi possível salvar a configuração: " + err.Error()), nil
	}
	if m.dictation.voiceAfterSetup {
		m.dictation.voiceAfterSetup = false
		label := strings.TrimSpace(msg.label)
		if label == "" {
			label = "Reconhecimento de fala"
		}
		return m.enableVoiceMode(label + " pronto. ")
	}
	return m.showTransientNoticeInline("Ditado local pronto. Rode /voice para começar a ditar.", transientNoticeSuccess), nil
}

// applyEngineComponents persists and applies resolved engine/model paths to the
// live config (shared by a fresh download and the already-installed fast path).
// Every installed model gets live text (true streaming or rolling), so the
// live pipeline is always switched on here.
func (m model) applyEngineComponents(comp dictation.EngineComponents) (model, error) {
	if path := m.dictation.userConfigPath; path != "" {
		if _, err := config.SetSTTLocalEngine(path, comp.BinaryPath, comp.ServerPath, comp.ModelPath, true); err != nil {
			return m, err
		}
	}
	m.dictation.cfg.Provider = config.STTProviderLocal
	m.dictation.cfg.LocalBinary = comp.BinaryPath
	m.dictation.cfg.LocalServerBinary = comp.ServerPath
	m.dictation.cfg.LocalModelPath = comp.ModelPath
	s := true
	m.dictation.cfg.Streaming = &s
	return m, nil
}

// appendDictationNotice appends a transcript notice, suppressing an immediate
// repeat of the same message — the fix for hammering the dictation gesture against an unusable
// model (or in voice mode) spamming the identical line on every keypress. It is
// stateless: it simply skips the append when the message equals the most recent
// transcript line, so an unrelated line in between lets it show again.
func (m model) appendDictationNotice(key, text string) model {
	_ = key
	if n := len(m.transcript); n > 0 && strings.TrimSpace(m.transcript[n-1].text) == strings.TrimSpace(text) {
		return m
	}
	return m.appendSystemNotice(text)
}
