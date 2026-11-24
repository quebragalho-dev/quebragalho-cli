package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Cmd+clique num link de arquivo. O protocolo SGR do mouse não tem bit para o
// Command: o Ghostty entrega Cmd+clique como clique simples quando o programa
// captura o mouse, e Opção+clique já chega. Por isso, num press esquerdo sem
// Ctrl/Alt, o qg pergunta ao sistema se o Command está pressionado, mas só no
// Ghostty: VS Code, iTerm2 e Terminal.app já abrem o link sozinhos no
// Cmd+clique, e consultar ali abriria o arquivo duas vezes.

// commandHeldAtClick diz se o Command está pressionado no press esquerdo. Em
// SSH o teclado é de outra máquina, e fora do Ghostty o próprio terminal trata
// o Cmd+clique: nesses casos nem consulta. Var para o teste trocar.
var commandHeldAtClick = func() bool {
	if remoteSession() || !ghosttyTerminal(os.Getenv) {
		return false
	}
	return systemCommandHeld()
}

// remoteSession diz se o qg roda por SSH.
func remoteSession() bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// ghosttyTerminal diz se o terminal da sessão é o Ghostty. A ordem importa:
//
//  1. TERM_PROGRAM contém "ghostty" (sem diferenciar maiúsculas): sim.
//  2. TERM_PROGRAM preenchido e diferente de "tmux": não. Um VS Code aberto a
//     partir do Ghostty herda GHOSTTY_RESOURCES_DIR, mas o terminal é o VS Code.
//  3. Sem TERM_PROGRAM útil (vazio ou tmux): sim se TERM contém "ghostty" ou
//     GHOSTTY_RESOURCES_DIR está preenchido. Dentro do tmux, TERM_PROGRAM vale
//     "tmux" e a variável herdada do Ghostty continua valendo.
func ghosttyTerminal(getenv func(string) string) bool {
	program := strings.ToLower(getenv("TERM_PROGRAM"))
	if strings.Contains(program, "ghostty") {
		return true
	}
	if program != "" && program != "tmux" {
		return false
	}
	return strings.Contains(strings.ToLower(getenv("TERM")), "ghostty") || getenv("GHOSTTY_RESOURCES_DIR") != ""
}

// linkClickMod é o modificador efetivo do clique para links de arquivo: um
// press esquerdo sem Ctrl/Alt, com o Command pressionado, ganha ModMeta.
// Movimento, rolagem e soltura não consultam o sistema. O chip de anexo não
// usa esta função, então Cmd+clique nele continua abrindo o anexo.
func linkClickMod(msg tea.MouseMsg) tea.KeyMod {
	mod := mouseEvent(msg).Mod
	if mouseLeftPress(msg) && !fileLinkClickModifier(mod) && commandHeldAtClick() {
		mod |= tea.ModMeta
	}
	return mod
}
