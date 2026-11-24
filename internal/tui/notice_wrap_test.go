package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// noticeLines splits a rendered notice into its visible lines with ANSI
// sequences removed, so width and content assertions see what the terminal
// shows.
func noticeLines(rendered string) []string {
	plain := ansi.Strip(rendered)
	return strings.Split(plain, "\n")
}

// assertNoticeFitsWidth fails when any rendered line is wider than width.
func assertNoticeFitsWidth(t *testing.T, rendered string, width int) {
	t.Helper()
	for index, line := range noticeLines(rendered) {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d is %d cells wide, want <= %d:\n%q", index, got, width, line)
		}
	}
}

// noticeBody rejoins the visible text of a rendered notice, dropping border
// glyphs and collapsing runs of spaces, so a wrapped word can be searched for
// across lines.
func noticeBody(rendered string) string {
	trimmed := strings.NewReplacer(
		"╭", "", "╮", "", "╰", "", "╯", "", "│", "", "─", "",
	).Replace(ansi.Strip(rendered))
	return strings.Join(strings.Fields(trimmed), " ")
}

// A notice longer than the box must wrap onto several lines instead of being
// cut with an ellipsis: the actionable tail of the message has to survive.
func TestRenderErrorRowWrapsLongNotice(t *testing.T) {
	const width = 60
	text := "Falha ao ler a imagem da área de transferência: the clipboard " +
		"holds an image but no helper on this host can read it; on macOS, " +
		"grant screen-recording permission to your terminal app"

	rendered := renderErrorRow(transcriptRow{kind: rowError, text: text}, width)

	if strings.Contains(rendered, "…") {
		t.Fatalf("notice must wrap instead of truncating, got:\n%s", rendered)
	}
	assertNoticeFitsWidth(t, rendered, width)

	body := noticeBody(rendered)
	for _, want := range []string{
		"Falha ao ler a imagem",
		"transferência",
		"no helper on this host can read it",
		"grant screen-recording permission to your terminal app",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered notice lost %q:\n%s", want, rendered)
		}
	}
	if lines := len(noticeLines(rendered)); lines < 4 {
		t.Fatalf("expected the notice to occupy several lines, got %d:\n%s", lines, rendered)
	}
}

// A single word wider than the box (a long path, a URL) still has to show in
// full: it is cut across lines rather than dropped behind an ellipsis.
func TestRenderErrorRowWrapsOverlongWord(t *testing.T) {
	const width = 60
	word := "caminho/" + strings.Repeat("segmento-muito-longo/", 6) + "arquivo.png"
	rendered := renderErrorRow(transcriptRow{kind: rowError, text: "falha ao abrir " + word}, width)

	if strings.Contains(rendered, "…") {
		t.Fatalf("overlong word must be hard-wrapped, not truncated, got:\n%s", rendered)
	}
	assertNoticeFitsWidth(t, rendered, width)

	joined := strings.ReplaceAll(noticeBody(rendered), " ", "")
	if !strings.Contains(joined, word) {
		t.Fatalf("overlong word lost characters when wrapped, got:\n%s", rendered)
	}
}

// Wide glyphs (accents and CJK) must be measured by display width, so a
// wrapped line never overflows the box or drops a character.
func TestRenderErrorRowWrapsWideGlyphs(t *testing.T) {
	const width = 60
	text := "Ação não concluída — a área de transferência está vazia; " +
		"verifique a permissão de leitura 日本語のテキストをここに置きます " +
		"e tente novamente com uma imagem menor 🖼️"
	rendered := renderErrorRow(transcriptRow{kind: rowError, text: text}, width)

	if strings.Contains(rendered, "…") {
		t.Fatalf("notice must wrap instead of truncating, got:\n%s", rendered)
	}
	assertNoticeFitsWidth(t, rendered, width)

	body := noticeBody(rendered)
	for _, want := range []string{
		"Ação não concluída",
		"transferência está vazia",
		"日本語のテキストをここに置きます",
		"tente novamente com uma imagem menor",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered notice lost %q:\n%s", want, rendered)
		}
	}
}

// The faint next-step hint under the box wraps too, so the guidance stays
// readable on a narrow terminal instead of ending in an ellipsis.
func TestRenderErrorRowWrapsHint(t *testing.T) {
	const width = 60
	hint := "instale xclip ou wl-clipboard e tente de novo — no macOS, conceda " +
		"permissão de gravação de tela ao terminal"
	rendered := renderErrorRow(transcriptRow{
		kind: rowError,
		text: "Falha ao ler a imagem da área de transferência: sem ferramenta",
		hint: hint,
	}, width)

	if strings.Contains(rendered, "…") {
		t.Fatalf("hint must wrap instead of truncating, got:\n%s", rendered)
	}
	assertNoticeFitsWidth(t, rendered, width)

	body := noticeBody(rendered)
	for _, want := range []string{
		"→ instale xclip ou wl-clipboard",
		"conceda permissão de gravação de tela ao terminal",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered hint lost %q:\n%s", want, rendered)
		}
	}
}

// A notice that already fits keeps its single line: wrapping must not reflow
// short messages.
func TestRenderErrorRowKeepsShortNoticeOnOneLine(t *testing.T) {
	rendered := renderErrorRow(transcriptRow{kind: rowError, text: "falha ao abrir imagem"}, 60)
	if lines := len(noticeLines(rendered)); lines != 3 {
		t.Fatalf("short notice should stay a 3-line box (top, text, bottom), got %d:\n%s", lines, rendered)
	}
	if !strings.Contains(noticeBody(rendered), "falha ao abrir imagem") {
		t.Fatalf("short notice text missing:\n%s", rendered)
	}
}
