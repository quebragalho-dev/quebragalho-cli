package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// file_links_command_test.go cobre o Cmd+clique: o terminal não entrega o
// Command no protocolo do mouse, então o qg pergunta ao sistema no press
// esquerdo, e só no Ghostty e em sessão local.

// withCommandHeld troca a consulta do Command pelo valor dado e conta as chamadas.
func withCommandHeld(t *testing.T, held bool) *int {
	t.Helper()
	calls := new(int)
	prev := commandHeldAtClick
	t.Cleanup(func() { commandHeldAtClick = prev })
	commandHeldAtClick = func() bool {
		*calls++
		return held
	}
	return calls
}

// withSystemCommand troca só a leitura do sistema, deixando o portão real
// (SSH, terminal e press esquerdo) em pé.
func withSystemCommand(t *testing.T, held bool) *int {
	t.Helper()
	calls := new(int)
	prev := systemCommandHeld
	t.Cleanup(func() { systemCommandHeld = prev })
	systemCommandHeld = func() bool {
		*calls++
		return held
	}
	return calls
}

// cleanSessionEnv zera as variáveis de SSH e de terminal para o teste não
// depender de onde a suíte roda.
func cleanSessionEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "TERM_PROGRAM", "TERM", "GHOSTTY_RESOURCES_DIR"} {
		t.Setenv(key, "")
	}
}

// inGhostty põe a sessão no Ghostty (depois de cleanSessionEnv).
func inGhostty(t *testing.T) {
	t.Helper()
	t.Setenv("TERM_PROGRAM", "ghostty")
}

func TestCommandClickOpensFileLink(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	withCommandHeld(t, true)
	opened := recordAttachmentOpens(t)
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "veja a.png aqui")

	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	updated, cmd := m.Update(testMouseClick(tea.MouseLeft, x, y))
	next := updated.(model)

	if cmd == nil {
		t.Fatal("Cmd+clique num link deve devolver o comando que abre o arquivo")
	}
	execCmd(cmd)
	wantOpened(t, opened, filepath.Join(dir, "a.png"))
	if next.transcriptSelection.active {
		t.Fatal("Cmd+clique num link deve abrir o arquivo, não começar seleção")
	}
}

func TestPlainClickWithoutCommandKeepsSelection(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	withCommandHeld(t, false)
	opened := recordAttachmentOpens(t)
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "veja a.png aqui")

	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	updated, _ := m.Update(testMouseClick(tea.MouseLeft, x, y))
	next := updated.(model)

	wantNothingOpened(t, opened)
	if !next.transcriptSelection.active {
		t.Fatal("clique sem Command sobre o link deve seguir como seleção")
	}
}

// Cmd sobre texto que não é link não abre nada: o comportamento atual (seleção)
// continua.
func TestCommandClickOutsideLinkStillSelects(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	withCommandHeld(t, true)
	opened := recordAttachmentOpens(t)
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "texto comum sem arquivo aqui")

	x, y := transcriptLineMousePoint(t, m, "comum", 1)
	updated, _ := m.Update(testMouseClick(tea.MouseLeft, x, y))
	next := updated.(model)

	wantNothingOpened(t, opened)
	if !next.transcriptSelection.active {
		t.Fatal("Cmd+clique fora de link deve manter a seleção de hoje")
	}
}

// Em SSH o teclado é de outra máquina: a consulta do sistema nem acontece, mesmo
// dentro do Ghostty.
func TestCommandQueryIsSkippedOverSSH(t *testing.T) {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Run(key, func(t *testing.T) {
			cleanSessionEnv(t)
			inGhostty(t)
			t.Setenv(key, "1")
			calls := withSystemCommand(t, true)

			if commandHeldAtClick() {
				t.Fatal("em SSH o Command nunca conta como pressionado")
			}
			if *calls != 0 {
				t.Fatalf("consultou o sistema %d vezes em SSH, want 0", *calls)
			}
		})
	}
}

// Fora do Ghostty o terminal já abre o link no Cmd+clique: o qg não consulta,
// para não abrir o arquivo duas vezes.
func TestCommandQueryIsSkippedOutsideGhostty(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"VS Code", map[string]string{"TERM_PROGRAM": "vscode"}},
		{"iTerm2", map[string]string{"TERM_PROGRAM": "iTerm.app"}},
		{"Terminal.app", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}},
		{"tmux fora do Ghostty", map[string]string{"TERM_PROGRAM": "tmux", "TERM": "screen"}},
		{"VS Code aberto pelo Ghostty", map[string]string{"TERM_PROGRAM": "vscode", "GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources/ghostty"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanSessionEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			calls := withSystemCommand(t, true)

			if commandHeldAtClick() {
				t.Fatal("fora do Ghostty o Cmd+clique segue o terminal, não o qg")
			}
			if *calls != 0 {
				t.Fatalf("consultou o sistema %d vezes fora do Ghostty, want 0", *calls)
			}
		})
	}
}

// Sessão local no Ghostty consulta o sistema uma vez por press esquerdo.
func TestCommandQueryUsesSystemOnLocalSession(t *testing.T) {
	cleanSessionEnv(t)
	inGhostty(t)
	calls := withSystemCommand(t, true)

	if !commandHeldAtClick() {
		t.Fatal("sessão local no Ghostty com Command pressionado deve responder true")
	}
	if *calls != 1 {
		t.Fatalf("consultas = %d, want 1", *calls)
	}
}

// Tabela da detecção do Ghostty, na ordem: TERM_PROGRAM primeiro, depois a
// herança de GHOSTTY_RESOURCES_DIR e TERM.
func TestGhosttyTerminalDetection(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"TERM_PROGRAM ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, true},
		{"TERM_PROGRAM com maiúsculas", map[string]string{"TERM_PROGRAM": "Ghostty"}, true},
		{"VS Code herda GHOSTTY_RESOURCES_DIR", map[string]string{"TERM_PROGRAM": "vscode", "GHOSTTY_RESOURCES_DIR": "/x"}, false},
		{"iTerm2", map[string]string{"TERM_PROGRAM": "iTerm.app"}, false},
		{"Terminal.app com TERM ghostty", map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-ghostty"}, false},
		{"tmux dentro do Ghostty, com GHOSTTY_RESOURCES_DIR", map[string]string{"TERM_PROGRAM": "tmux", "GHOSTTY_RESOURCES_DIR": "/x"}, true},
		{"tmux dentro do Ghostty, com TERM ghostty", map[string]string{"TERM_PROGRAM": "tmux", "TERM": "xterm-ghostty"}, true},
		{"tmux fora do Ghostty", map[string]string{"TERM_PROGRAM": "tmux", "TERM": "screen"}, false},
		{"sem TERM_PROGRAM, TERM ghostty", map[string]string{"TERM": "xterm-ghostty"}, true},
		{"sem TERM_PROGRAM, GHOSTTY_RESOURCES_DIR", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, true},
		{"sem nada", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ghosttyTerminal(func(key string) string { return tc.env[key] })
			if got != tc.want {
				t.Fatalf("ghosttyTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

// Movimento, rolagem e soltura nunca consultam: só o press esquerdo.
func TestCommandQueryOnlyOnLeftPress(t *testing.T) {
	cleanSessionEnv(t)
	inGhostty(t)
	calls := withSystemCommand(t, true)
	m := mouseTestModel()
	m.mouseCapture = true

	events := []tea.Msg{
		tea.MouseMotionMsg(tea.Mouse{Button: tea.MouseNone, X: 2, Y: 2}),
		tea.MouseMotionMsg(tea.Mouse{Button: tea.MouseLeft, X: 2, Y: 2}),
		tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelUp, X: 2, Y: 2}),
		tea.MouseReleaseMsg(tea.Mouse{Button: tea.MouseLeft, X: 2, Y: 2}),
	}
	for _, msg := range events {
		m.Update(msg)
	}
	if *calls != 0 {
		t.Fatalf("movimento, rolagem e soltura consultaram o sistema %d vezes, want 0", *calls)
	}
}

// Link quebrado em duas linhas, aberto com Cmd: a janela usa o mesmo
// modificador efetivo que o Ctrl.
func TestCommandClickOpensWrappedFileLink(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov"))
	cleanSessionEnv(t)
	inGhostty(t)
	withSystemCommand(t, true)
	plain := []string{
		"veja " + filepath.Join(dir, "e2e-links", "work", "Gravação"),
		"de Tela teste.mov aqui",
	}
	rows := []transcriptSelectableLine{
		{bodyY: 10, text: plain[0]},
		{bodyY: 11, text: plain[1]},
	}
	opened := recordAttachmentOpens(t)

	col := strings.Index(rows[1].text, "Tela") + 1
	mod := linkClickMod(tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft}))
	handled, err := runFileLinkClick(rows, 1, col, dir, mod)
	if err != nil {
		t.Fatalf("opener error: %v", err)
	}
	if !handled {
		t.Fatal("Cmd+clique numa peça do link quebrado deve ser tratado")
	}
	wantOpened(t, opened, filepath.Join(dir, "e2e-links", "work", "Gravação de Tela teste.mov"))
}

// O chip de anexo continua abrindo com Cmd: o portão do link só vale para o
// modificador efetivo, não para a tecla vista pelo chip.
func TestCommandClickOnChipStillOpensAttachment(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	withCommandHeld(t, true)
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)

	clickChipAndRun(t, m, 3, 1)

	wantOpenedResolved(t, opened, png)
}
