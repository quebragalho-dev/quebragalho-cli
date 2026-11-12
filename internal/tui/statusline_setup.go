package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// statuslineProposal is what the /statusline setup turn writes to the staging
// proposal.json: a field layout and/or a staged script. It mirrors the config
// shape so validation maps one to one onto StatusLineConfig.
type statuslineProposal struct {
	Lines     []config.StatusLineLine `json:"lines"`
	Separator string                  `json:"separator"`
	// Script names a file inside the staging dir (e.g. "ctx.sh"), never a
	// path: confinedStagingPath keeps it inside staging.
	Script    string `json:"script"`
	TimeoutMs int    `json:"timeoutMs"`
	RefreshMs int    `json:"refreshMs"`
}

// validatedConfig drops unknown ids (reported), adjusts cosmetic values
// (reported), and builds the config the preview renders. A proposal with
// nothing valid left previews empty, which the preview marks instead of
// applying a blank line.
func (p statuslineProposal) validatedConfig() (config.StatusLineConfig, []string) {
	cfg := config.StatusLineConfig{Separator: p.Separator, TimeoutMs: p.TimeoutMs, RefreshMs: p.RefreshMs}
	var warnings []string
	for _, line := range p.Lines {
		kept := config.StatusLineLine{}
		for _, id := range line.Left {
			if statuslineFieldKnown(id) {
				kept.Left = append(kept.Left, id)
			} else if strings.TrimSpace(id) != "" {
				warnings = append(warnings, "campo desconhecido ignorado: "+id)
			}
		}
		for _, id := range line.Right {
			if statuslineFieldKnown(id) {
				kept.Right = append(kept.Right, id)
			} else if strings.TrimSpace(id) != "" {
				warnings = append(warnings, "campo desconhecido ignorado: "+id)
			}
		}
		cfg.Lines = append(cfg.Lines, kept)
	}
	adjusted, adjustWarnings := cfg.Adjusted()
	return adjusted, append(warnings, adjustWarnings...)
}

// statuslineSetupState tracks an in-flight /statusline setup turn: the run to
// finalize when it lands, and the staging dir that is fresh from startedAt on.
type statuslineSetupState struct {
	runID      int
	startedAt  time.Time
	stagingDir string
}

// statuslinePendingApply waits for Enter (apply) or Esc (cancel) after the
// preview rendered with real data. While validating is set the preview is
// still being dry-run; Enter waits instead of applying a guess.
type statuslinePendingApply struct {
	proposal     statuslineProposal
	cfg          config.StatusLineConfig
	preview      string
	stagingDir   string
	scriptStaged string
	warnings     []string
	validating   bool
}

// statuslineSetupValidatedMsg carries the staged-script dry run back: lines
// on success, err on timeout/failure/empty output.
type statuslineSetupValidatedMsg struct {
	lines []string
	err   string
}

// statuslineStagingDir resolves <userConfigDir>/qg/statusline/staging.
// Empty userConfigPath (tests) refuses: a setup turn must never stage into
// an unknown tree.
func statuslineStagingDir(userConfigPath string) (string, error) {
	trimmed := strings.TrimSpace(userConfigPath)
	if trimmed == "" {
		return "", fmt.Errorf("sem config do usuário: a proposta da statusline precisa de um lugar para o staging")
	}
	return filepath.Join(filepath.Dir(trimmed), "statusline", "staging"), nil
}

// statuslineFinalDir is where approved scripts live: <userConfigDir>/qg/statusline.
func statuslineFinalDir(userConfigPath string) (string, error) {
	staging, err := statuslineStagingDir(userConfigPath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(staging), nil
}

// confinedStagingPath keeps a model-proposed filename inside the staging dir:
// base name only, no separators or traversal, and the joined result must stay
// under staging. Never resolve-then-open (check-to-use): the caller opens the
// returned path directly.
func confinedStagingPath(stagingDir, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != filepath.Base(trimmed) {
		return "", fmt.Errorf("nome de script inválido %q: use só o nome do arquivo", name)
	}
	if strings.ContainsAny(trimmed, `/\`) || trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("nome de script inválido %q: sem barras ou pontos", name)
	}
	candidate := filepath.Join(stagingDir, trimmed)
	if !strings.HasSuffix(filepath.Clean(candidate), string(filepath.Separator)+trimmed) {
		return "", fmt.Errorf("nome de script inválido %q", name)
	}
	return candidate, nil
}

// statuslineSetupPrompt builds the dedicated setup-turn instruction: catalog,
// live config, a real stdin example, width, and OS, plus the staging rules.
func (m model) statuslineSetupPrompt(stagingDir string) string {
	current, _ := json.Marshal(m.statuslineCfg)
	example := string(m.statuslineStdin(maxInt(m.width, 80)))
	var b strings.Builder
	b.WriteString("Você vai montar a statusline do QG (a linha de rodapé do TUI). REGRAS DURAS:\n")
	b.WriteString("1. NUNCA escreva na config viva. Proposta vai em " + filepath.Join(stagingDir, "proposal.json") + " e script SÓ dentro de " + stagingDir + ".\n")
	b.WriteString("2. Campos válidos (stable, inglês): " + strings.Join(statuslineFieldIDs, ", ") + ". Qualquer outro id é ignorado com aviso.\n")
	b.WriteString("3. proposal.json espelha a config: {\"lines\": [{\"left\": [...], \"right\": [...]}], \"separator\": \" | \", \"script\": \"nome.sh\", \"timeoutMs\": 2000, \"refreshMs\": 5000}. Até 3 linhas. Sem script = só campos.\n")
	b.WriteString("4. Script: saída até 3 linhas, rápido (< 2s), sem rede, sem segredos. Recebe no stdin o JSON abaixo.")
	b.WriteString(" Escreva na sintaxe do SO de destino — sh (linux/macOS) ou cmd (windows); o QG executa via `sh -c` no linux/macOS e via `cmd /C` no windows.")
	b.WriteString(" SO desta sessão: " + runtime.GOOS + ".\n")
	b.WriteString("5. Não mostre a proposta no chat; o QG valida, renderiza a prévia com dados reais e pergunta ao usuário (Enter aplica, Esc cancela).\n\n")
	b.WriteString("Config atual: " + string(current) + "\n")
	b.WriteString("O stdin do comando tem SEMPRE estas chaves (null quando desconhecido): session_id, cwd, model, provider, effort, context{used, window, pct}, cache{hit_pct}, cost_brl, branch, version, width.\n")
	b.WriteString("Exemplo real do stdin (desta sessão): " + example + "\n")
	b.WriteString("Largura do terminal: " + strconv.Itoa(maxInt(m.width, 80)) + ". SO: " + runtime.GOOS + ".\n\n")
	b.WriteString("Pedido do usuário: ")
	return b.String()
}

// handleStatuslineCommand implements /statusline: bare shows the status card,
// "reset" clears the user customization, anything else starts a setup turn
// with the model (idle only).
func (m model) handleStatuslineCommand(arg string) (model, tea.Cmd) {
	switch normalized := strings.ToLower(strings.TrimSpace(arg)); normalized {
	case "", "list", "status":
		return m.appendStatuslineNotice(m.statuslineStatusText()), nil
	case "reset":
		return m.resetStatusline(), nil
	default:
		if m.exiting {
			return m, nil
		}
		if m.pending || m.compactInFlight {
			return m.appendStatuslineNotice("Statusline\ntermine ou cancele a execução atual antes de montar uma nova statusline."), nil
		}
		if m.statuslineSetup != nil {
			return m.appendStatuslineNotice("Statusline\njá existe uma montagem em andamento; aguarde o turno terminar."), nil
		}
		staging, err := statuslineStagingDir(m.userConfigPath)
		if err != nil {
			return m.appendStatuslineNotice("Statusline\n" + err.Error()), nil
		}
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível preparar o staging: " + err.Error()), nil
		}
		m.statuslineSetup = &statuslineSetupState{startedAt: m.now(), stagingDir: staging}
		prompt := m.statuslineSetupPrompt(staging) + arg
		next, cmd := m.launchPrompt(prompt)
		next.statuslineSetup = &statuslineSetupState{runID: next.activeRunID, startedAt: m.statuslineSetup.startedAt, stagingDir: staging}
		return next, cmd
	}
}

// abbreviateStatuslinePath replaces a home dir with ~ so cards stay short
// and portable across machines. It applies anywhere in the string on a
// separator boundary, since a command line embeds the path (sh '~/x.sh').
func abbreviateStatuslinePath(text string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = strings.ReplaceAll(text, home+string(os.PathSeparator), "~"+string(os.PathSeparator))
		if text == home {
			return "~"
		}
	}
	return text
}

// fitMiddleStatusline truncates line to max cells keeping the head and the
// tail (a script filename at the end survives a narrow card). Plain-text
// widths; the card styles afterwards.
func fitMiddleStatusline(line string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(line) <= max {
		return line
	}
	tailLen := max / 3
	if minTail := 20; tailLen < minTail {
		tailLen = minTail
	}
	if tailLen > max-2 {
		tailLen = max - 2
	}
	headLen := max - tailLen - 1
	if headLen < 1 {
		return "…" + tailCells(line, max-1)
	}
	return truncateDisplayWidth(line, headLen) + "…" + tailCells(line, tailLen)
}

// tailCells keeps the last max cells of s, rune-safe.
func tailCells(s string, max int) string {
	if max <= 0 {
		return ""
	}
	width, cut := 0, len(s)
	for cut > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:cut])
		if size == 0 {
			break
		}
		if width += lipgloss.Width(string(r)); width > max {
			break
		}
		cut -= size
	}
	return s[cut:]
}

// statuslineStatusText renders the /statusline status card: resolved layout,
// command, cadences, and any unknown ids in the current config.
func (m model) statuslineStatusText() string {
	cfg := m.statuslineCfg
	var lines []string
	if !cfg.IsCustom() {
		lines = append(lines, "layout: padrão (igual ao de hoje)")
	} else {
		for i, line := range cfg.Lines {
			lines = append(lines, fmt.Sprintf("linha %d: %s  |  %s", i+1,
				strings.Join(line.Left, ", "), strings.Join(line.Right, ", ")))
		}
		if cfg.Separator != "" {
			lines = append(lines, "separador: "+cfg.Separator)
		}
	}
	if cfg.Command != nil {
		// Long commands end-truncate in the card and lose the script name,
		// so shorten from the middle (keeping the tail) after ~/abbreviation.
		budget := maxInt(m.width, 80) - len("  comando: ") - 4
		lines = append(lines, "comando: "+fitMiddleStatusline(abbreviateStatuslinePath(cfg.Command.Command), budget))
		lines = append(lines, fmt.Sprintf("timeout: %dms · refresh: %dms",
			cfg.EffectiveTimeoutMs(), cfg.EffectiveRefreshMs()))
	}
	for _, unknown := range unknownStatusLineFields(cfg) {
		lines = append(lines, "aviso: campo desconhecido "+unknown)
	}
	return renderCommandOutput(commandOutput{
		Title:  "Statusline",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Estado",
			Lines: lines,
		}},
		Hints: []string{
			"use /statusline <pedido> para o modelo montar uma nova (com prévia)",
			"use /statusline reset para voltar ao padrão",
			"campos: " + strings.Join(statuslineFieldIDs, ", "),
		},
	})
}

// resetStatusline clears the user customization and the runner state. Project
// layout still applies underneath (aviso na inicialização já cobre o comando).
// An in-flight setup or pending preview is dropped too: reset means default,
// and its staging is discarded.
func (m model) resetStatusline() model {
	if m.statuslineSetup != nil {
		discardStatuslineStaging(m.statuslineSetup.stagingDir, "")
		m.statuslineSetup = nil
	}
	if m.statuslinePending != nil {
		discardStatuslineStaging(m.statuslinePending.stagingDir, m.statuslinePending.scriptStaged)
		m.statuslinePending = nil
	}
	if strings.TrimSpace(m.userConfigPath) != "" {
		if _, err := config.SetStatusLine(m.userConfigPath, config.StatusLineConfig{}); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível limpar a config: " + err.Error())
		}
	}
	m.statuslineCfg = config.StatusLineConfig{}
	m.statuslineCmdOutput = ""
	m.statuslineCmdErr = ""
	m.statuslineCmdInFlight = false
	m.statuslineCmdStartedAt = time.Time{}
	m.statuslineCmdSeq++
	m.statuslineTickArmed = false
	return m.appendStatuslineNotice("Statusline\nvoltou ao padrão.")
}

func (m model) appendStatuslineNotice(text string) model {
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}

// maybeFinalizeStatuslineSetup runs on every Update: when the setup turn's
// final message lands (normal finish or cancel flush, both flow through
// Update), the staging proposal validates and previews. Fields-only proposals
// preview synchronously; script proposals dispatch one dry run first.
func (m model) maybeFinalizeStatuslineSetup(msg tea.Msg) (model, tea.Cmd) {
	if m.statuslineSetup == nil {
		return m, nil
	}
	response, ok := msg.(agentResponseMsg)
	if !ok || response.runID != m.statuslineSetup.runID {
		return m, nil
	}
	setup := m.statuslineSetup
	m.statuslineSetup = nil
	proposal, err := readStatuslineProposal(setup.stagingDir, setup.startedAt)
	if err != nil {
		discardStatuslineStaging(setup.stagingDir, "")
		return m.appendStatuslineNotice("Statusline\no modelo não gerou proposta (" + err.Error() + "); staging descartado."), nil
	}
	cfg, warnings := proposal.validatedConfig()
	if proposal.Script != "" {
		scriptPath, err := confinedStagingPath(setup.stagingDir, proposal.Script)
		if err != nil {
			discardStatuslineStaging(setup.stagingDir, "")
			return m.appendStatuslineNotice("Statusline\nscript rejeitado (" + err.Error() + "); staging descartado."), nil
		}
		if info, err := os.Stat(scriptPath); err != nil || info.IsDir() || info.ModTime().Before(setup.startedAt) {
			discardStatuslineStaging(setup.stagingDir, "")
			return m.appendStatuslineNotice("Statusline\no script da proposta não está no staging; staging descartado."), nil
		}
		if err := os.Chmod(scriptPath, 0o700); err != nil {
			discardStatuslineStaging(setup.stagingDir, "")
			return m.appendStatuslineNotice("Statusline\nnão foi possível preparar o script: " + err.Error()), nil
		}
		timeout := time.Duration(maxInt(cfg.EffectiveTimeoutMs(), 1)) * time.Millisecond
		stdin := string(m.statuslineStdin(maxInt(m.width, 80)))
		env := statuslineChildEnv(maxInt(m.width, 80), m.height)
		pending := &statuslinePendingApply{proposal: proposal, cfg: cfg, stagingDir: setup.stagingDir, scriptStaged: scriptPath, warnings: warnings, validating: true}
		m.statuslinePending = pending
		return m, func() tea.Msg {
			valid := execStatuslineRun(context.Background(), statuslineInvokeScript(scriptPath), stdin, env, timeout)
			if valid.errNote != "" {
				return statuslineSetupValidatedMsg{err: valid.errNote}
			}
			if len(valid.lines) == 0 {
				return statuslineSetupValidatedMsg{err: "o script não imprimiu nada"}
			}
			return statuslineSetupValidatedMsg{lines: valid.lines}
		}
	}
	pending := &statuslinePendingApply{proposal: proposal, cfg: cfg, stagingDir: setup.stagingDir, warnings: warnings}
	pending.preview = m.previewStatuslineProposal(pending, nil)
	m.statuslinePending = pending
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: pending.preview})
	return m, nil
}

// readStatuslineProposal loads a fresh proposal.json: missing, stale (older
// than the setup turn), or invalid JSON all report an error.
func readStatuslineProposal(stagingDir string, since time.Time) (statuslineProposal, error) {
	raw, err := os.ReadFile(filepath.Join(stagingDir, "proposal.json"))
	if err != nil {
		return statuslineProposal{}, fmt.Errorf("sem proposal.json no staging")
	}
	var proposal statuslineProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return statuslineProposal{}, fmt.Errorf("proposal.json inválido: %v", err)
	}
	if info, err := os.Stat(filepath.Join(stagingDir, "proposal.json")); err != nil || info.ModTime().Before(since) {
		return statuslineProposal{}, fmt.Errorf("proposal.json é anterior ao turno")
	}
	cfg, _ := proposal.validatedConfig()
	if !cfg.IsCustom() && strings.TrimSpace(proposal.Script) == "" {
		return statuslineProposal{}, fmt.Errorf("proposta vazia")
	}
	return proposal, nil
}

// discardStatuslineStaging removes this setup's staging files: the proposal
// plus the staged script when named. Only known names inside staging — never
// a directory sweep.
func discardStatuslineStaging(stagingDir, scriptStaged string) {
	os.Remove(filepath.Join(stagingDir, "proposal.json"))
	if scriptStaged != "" {
		if clean, err := confinedStagingPath(stagingDir, filepath.Base(scriptStaged)); err == nil && clean == scriptStaged {
			os.Remove(clean)
		}
	}
}

// statuslineInvokeScript quotes a staged script path for the platform shell,
// matching the live runner (`sh -c` on Unix, `cmd /C` on Windows).
func statuslineInvokeScript(path string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(path, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// applyStatuslineSetupValidated folds the staged-script dry run: failure
// discards the staging with an explanation; success renders the preview and
// waits for Enter/Esc.
func (m model) applyStatuslineSetupValidated(msg statuslineSetupValidatedMsg) model {
	pending := m.statuslinePending
	if pending == nil || !pending.validating {
		return m
	}
	pending.validating = false
	if msg.err != "" {
		discardStatuslineStaging(pending.stagingDir, pending.scriptStaged)
		m.statuslinePending = nil
		return m.appendStatuslineNotice("Statusline\nscript rejeitado (" + msg.err + "); staging descartado.")
	}
	pending.cfg.Command = &config.StatusLineCommand{Command: statuslineInvokeScript(pending.scriptStaged)}
	pending.preview = m.previewStatuslineProposal(pending, msg.lines)
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: pending.preview})
	return m
}

func (m model) previewStatuslineProposal(pending *statuslinePendingApply, scriptLines []string) string {
	render := m
	render.statuslineCfg = pending.cfg
	if len(scriptLines) > 0 {
		render.statuslineCmdOutput = strings.Join(scriptLines, "\n")
	}
	width := maxInt(m.width, 80)
	var lines []string
	lines = append(lines, "Prévia com os dados desta sessão:")
	lines = append(lines, render.statusLine(width))
	lines = append(lines, "")
	for _, warning := range pending.warnings {
		lines = append(lines, "aviso: "+warning)
	}
	return renderCommandOutput(commandOutput{
		Title:  "Statusline",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Proposta",
			Lines: lines,
		}},
		Hints: []string{"Enter aplica · Esc cancela"},
	})
}

// handleStatuslinePendingKey owns the keyboard while a proposal preview is
// up: Enter applies, Esc (or Ctrl+C) discards the staging, anything else is
// swallowed so a stray keystroke never types into the composer behind the
// decision.
func (m model) handleStatuslinePendingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pending := m.statuslinePending
	if pending == nil {
		return m, nil
	}
	switch {
	case keyIs(msg, tea.KeyEnter):
		if pending.validating {
			return m.showTransientNoticeInline("Statusline: validando o script…", transientNoticeInfo), nil
		}
		return m.applyStatuslinePending()
	case keyIs(msg, tea.KeyEsc) || keyCtrl(msg, 'c'):
		discardStatuslineStaging(pending.stagingDir, pending.scriptStaged)
		m.statuslinePending = nil
		if keyCtrl(msg, 'c') {
			// Esc discards quietly; Ctrl+C discards and keeps its normal
			// meaning (first press arms the exit confirmation).
			m = m.appendStatuslineNotice("Statusline\nproposta descartada.")
			return m.handleCtrlC()
		}
		return m.appendStatuslineNotice("Statusline\nproposta descartada."), nil
	default:
		return m, nil
	}
}

// applyStatuslinePending moves the staged script to its final home, persists
// the proposal to the user config, and switches the live statusline over. A
// persist failure keeps the pending state so Enter retries; nothing is lost.
// When the applied config runs a command but no refresh chain is alive, the
// chain is armed (a run starts right away); a running chain is left alone.
func (m model) applyStatuslinePending() (model, tea.Cmd) {
	pending := m.statuslinePending
	if pending == nil {
		return m, nil
	}
	cfg := pending.cfg
	if pending.scriptStaged != "" {
		finalDir, err := statuslineFinalDir(m.userConfigPath)
		if err != nil {
			return m.appendStatuslineNotice("Statusline\n" + err.Error()), nil
		}
		if err := os.MkdirAll(finalDir, 0o700); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível preparar a pasta de scripts: " + err.Error()), nil
		}
		finalPath := filepath.Join(finalDir, filepath.Base(pending.scriptStaged))
		if err := os.Rename(pending.scriptStaged, finalPath); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível instalar o script: " + err.Error()), nil
		}
		cfg.Command = &config.StatusLineCommand{Command: statuslineInvokeScript(finalPath)}
	}
	if strings.TrimSpace(m.userConfigPath) != "" {
		if _, err := config.SetStatusLine(m.userConfigPath, cfg); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível salvar: " + err.Error()), nil
		}
	}
	discardStatuslineStaging(pending.stagingDir, "")
	m.statuslinePending = nil
	m.statuslineCfg = cfg
	// Drop anything the old config had in flight and restart the cadence, so
	// the new layout/refresh takes effect on the next Update, not whenever
	// the old window happened to elapse.
	m.statuslineCmdOutput = ""
	m.statuslineCmdErr = ""
	m.statuslineCmdInFlight = false
	m.statuslineCmdStartedAt = time.Time{}
	m.statuslineCmdSeq++
	m = m.appendStatuslineNotice("Statusline\natualizada.")
	if m.statuslineCommandActive() && !m.statuslineTickArmed {
		m.statuslineTickArmed = true
		return m, statuslineInitCmd()
	}
	return m, nil
}
