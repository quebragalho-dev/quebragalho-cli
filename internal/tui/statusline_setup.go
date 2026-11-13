package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
// shape so validation maps one to one onto StatusLineConfig. A proposal
// without a script keeps the current command and cadences; an explicit
// "command": null drops the command instead.
type statuslineProposal struct {
	Lines     []config.StatusLineLine `json:"lines"`
	Separator string                  `json:"separator"`
	// Command carries explicit null to drop the current command. Absent
	// keeps the current command and cadences (see validatedConfig). A
	// literal command object is never honored: a new command only enters
	// through a staged script, which passes the dry run and installs
	// inside the scripts dir. A staged script (Script, below) always wins.
	Command json.RawMessage `json:"command,omitempty"`
	// Script names a file inside the staging dir (e.g. "ctx.sh"), never a
	// path: confinedStagingPath keeps it inside staging.
	Script    string `json:"script"`
	TimeoutMs int    `json:"timeoutMs"`
	RefreshMs int    `json:"refreshMs"`
}

// validatedConfig drops unknown ids (reported), adjusts cosmetic values
// (reported), and builds the config the preview renders. Fields-only
// proposals keep the current command and cadences; an explicit null command
// drops it. A proposal with nothing valid left previews empty, which the
// preview marks instead of applying a blank line.
func (p statuslineProposal) validatedConfig(current config.StatusLineConfig) (config.StatusLineConfig, []string) {
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
	switch {
	case p.Script != "":
		// Installed on apply; the dry run gates it before any preview.
	case p.Command == nil:
		cfg.Command = current.Command
		if cfg.TimeoutMs == 0 {
			cfg.TimeoutMs = current.TimeoutMs
		}
		if cfg.RefreshMs == 0 {
			cfg.RefreshMs = current.RefreshMs
		}
	default:
		if trimmed := strings.TrimSpace(string(p.Command)); trimmed == "null" {
			warnings = append(warnings, "comando removido a pedido da proposta")
		} else {
			// A literal command object is never honored and never dry-run:
			// a new command only enters through a staged script. Keep the
			// current command and say so.
			warnings = append(warnings, "comando literal na proposta ignorado: comando novo só entra por script no staging")
			cfg.Command = current.Command
		}
	}
	adjusted, adjustWarnings := cfg.Adjusted()
	return adjusted, append(warnings, adjustWarnings...)
}

// statuslineSetupState tracks an in-flight /statusline setup turn: the run to
// finalize when it lands, the staging dir that is fresh from startedAt on,
// and the dir's identity at turn start. Every later touch must resolve to
// that same directory: a mid-turn swap for a link (or another dir) refuses
// before anything is read, chmodded, executed, or discarded.
type statuslineSetupState struct {
	runID       int
	startedAt   time.Time
	stagingDir  string
	stagingInfo os.FileInfo
}

// statuslinePendingApply waits for Enter (apply) or Esc (cancel) after the
// preview rendered with real data. While validating is set the preview is
// still being dry-run; Enter waits instead of applying a guess.
type statuslinePendingApply struct {
	proposal   statuslineProposal
	cfg        config.StatusLineConfig
	preview    string
	stagingDir string
	// stagingGuard is the staging dir identity from turn start: every
	// discard re-verifies it before touching a file.
	stagingGuard os.FileInfo
	scriptStaged string
	// scriptBody holds the verified bytes the dry run executed. Apply
	// installs exactly these, never re-reading the staging dir, so a
	// swap between the dry run and Enter cannot substitute content.
	scriptBody []byte
	// scriptCopy is the private dry-run file (outside staging, removed
	// with the staging on every discard path).
	scriptCopy string
	warnings   []string
	validating bool
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
	b.WriteString("3. proposal.json espelha a config: {\"lines\": [{\"left\": [...], \"right\": [...]}], \"separator\": \" | \", \"script\": \"nome.sh\", \"command\": null, \"timeoutMs\": 2000, \"refreshMs\": 5000}. Até 3 linhas. Sem script e sem \"command\", a proposta MANTÉM o comando e os tempos atuais; com \"command\": null explícito o comando sai.\n")
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
		// Never stage through a link: the scripts dir must be a real
		// directory, or proposals could land outside the user config.
		// The dir identity is guarded: every later touch in this turn
		// must resolve to this same directory.
		stagingInfo, err := os.Lstat(staging)
		if err != nil || !stagingInfo.IsDir() || stagingInfo.Mode()&os.ModeSymlink != 0 {
			return m.appendStatuslineNotice("Statusline\nstaging inválido: não é um diretório"), nil
		}
		m.statuslineSetup = &statuslineSetupState{startedAt: m.now(), stagingDir: staging, stagingInfo: stagingInfo}
		prompt := m.statuslineSetupPrompt(staging) + arg
		next, cmd := m.launchPrompt(prompt)
		next.statuslineSetup = &statuslineSetupState{runID: next.activeRunID, startedAt: m.statuslineSetup.startedAt, stagingDir: staging, stagingInfo: stagingInfo}
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
		discardStatuslineStaging(m.statuslineSetup.stagingDir, m.statuslineSetup.stagingInfo, "", "")
		m.statuslineSetup = nil
	}
	if m.statuslinePending != nil {
		discardStatuslineStaging(m.statuslinePending.stagingDir, m.statuslinePending.stagingGuard, m.statuslinePending.scriptStaged, m.statuslinePending.scriptCopy)
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
	m = m.cancelStatuslineRun()
	m.statuslineCmdStartedAt = time.Time{}
	m.statuslineCmdSeq++
	// The old cadence dies here: any tick armed before the reset carries the
	// previous generation and is discarded on arrival.
	m.statuslineTickGen++
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
	// The staging dir may have been swapped mid-turn: open it
	// handle-anchored and re-verify its identity before reading anything
	// through it. A planted link is removed here (the link itself);
	// nothing outside is touched.
	root, err := openVerifiedStagingRoot(setup.stagingDir, setup.stagingInfo)
	if err != nil {
		return m.appendStatuslineNotice("Statusline\n" + err.Error()), nil
	}
	defer root.Close()
	proposal, err := readStatuslineProposal(root, setup.startedAt)
	if err != nil {
		discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
		return m.appendStatuslineNotice("Statusline\no modelo não gerou proposta (" + err.Error() + "); staging descartado."), nil
	}
	cfg, warnings := proposal.validatedConfig(m.statuslineCfg)
	if proposal.Script != "" {
		scriptPath, err := confinedStagingPath(setup.stagingDir, proposal.Script)
		if err != nil {
			discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
			return m.appendStatuslineNotice("Statusline\nscript rejeitado (" + err.Error() + "); staging descartado."), nil
		}
		scriptFile, info, err := openRootFileChecked(root, filepath.Base(scriptPath))
		if err != nil {
			discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
			return m.appendStatuslineNotice("Statusline\nscript rejeitado (" + err.Error() + "); staging descartado."), nil
		}
		body, err := io.ReadAll(scriptFile)
		scriptFile.Close()
		if err != nil {
			discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
			return m.appendStatuslineNotice("Statusline\nscript rejeitado (ilegível); staging descartado."), nil
		}
		if info.ModTime().Before(setup.startedAt) {
			discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
			return m.appendStatuslineNotice("Statusline\no script da proposta não está no staging; staging descartado."), nil
		}
		// The dry run executes a private copy of the verified bytes, and
		// apply installs those same bytes from memory: neither chmods,
		// executes, nor re-reads anything through the staging dir.
		copyPath, err := writeStatuslineDryRunCopy(m.userConfigPath, body)
		if err != nil {
			discardStatuslineStaging(setup.stagingDir, setup.stagingInfo, "", "")
			return m.appendStatuslineNotice("Statusline\n" + err.Error()), nil
		}
		timeout := time.Duration(maxInt(cfg.EffectiveTimeoutMs(), 1)) * time.Millisecond
		stdin := string(m.statuslineStdin(maxInt(m.width, 80)))
		env := statuslineChildEnv(maxInt(m.width, 80), m.height)
		pending := &statuslinePendingApply{proposal: proposal, cfg: cfg, stagingDir: setup.stagingDir, stagingGuard: setup.stagingInfo, scriptStaged: scriptPath, scriptBody: body, scriptCopy: copyPath, warnings: warnings, validating: true}
		m.statuslinePending = pending
		return m, func() tea.Msg {
			valid := execStatuslineRun(context.Background(), statuslineInvokeScript(copyPath), stdin, env, timeout)
			if valid.errNote != "" {
				return statuslineSetupValidatedMsg{err: valid.errNote}
			}
			if len(valid.lines) == 0 {
				return statuslineSetupValidatedMsg{err: "o script não imprimiu nada"}
			}
			return statuslineSetupValidatedMsg{lines: valid.lines}
		}
	}
	pending := &statuslinePendingApply{proposal: proposal, cfg: cfg, stagingDir: setup.stagingDir, stagingGuard: setup.stagingInfo, warnings: warnings}
	pending.preview = m.previewStatuslineProposal(pending, nil)
	m.statuslinePending = pending
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: pending.preview})
	return m, nil
}

// readStatuslineProposal loads a fresh proposal.json through the verified
// staging root: missing, stale (older than the setup turn), or invalid JSON
// all report an error. Every touch stays anchored: Lstat gates regular
// files, the open never leaves the root, and the modtime comes from the
// open file.
func readStatuslineProposal(root *os.Root, since time.Time) (statuslineProposal, error) {
	file, _, err := openRootFileChecked(root, "proposal.json")
	if err != nil {
		if os.IsNotExist(err) {
			return statuslineProposal{}, fmt.Errorf("sem proposal.json no staging")
		}
		return statuslineProposal{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return statuslineProposal{}, fmt.Errorf("proposal.json ilegível: %v", err)
	}
	var proposal statuslineProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return statuslineProposal{}, fmt.Errorf("proposal.json inválido: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		return statuslineProposal{}, fmt.Errorf("proposal.json ilegível: %v", err)
	}
	if info.ModTime().Before(since) {
		return statuslineProposal{}, fmt.Errorf("proposal.json é anterior ao turno")
	}
	cfg, _ := proposal.validatedConfig(config.StatusLineConfig{})
	if !cfg.IsCustom() && strings.TrimSpace(proposal.Script) == "" {
		return statuslineProposal{}, fmt.Errorf("proposta vazia")
	}
	return proposal, nil
}

// openRootFileChecked opens one known staging entry through the verified
// root: Lstat refuses links and non-regular files, Open stays anchored to
// the guarded directory, and fstat confirms the opened file is regular.
// A kernel symlink refusal (see isSymlinkRefusal) maps to the same refusal.
func openRootFileChecked(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	if info, err := root.Lstat(name); err != nil {
		return nil, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, statuslineRefusalError(name, true)
	} else if !info.Mode().IsRegular() {
		return nil, nil, statuslineRefusalError(name, false)
	}
	file, err := root.Open(name)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, nil, statuslineRefusalError(name, true)
		}
		return nil, nil, err
	}
	if info, err := file.Stat(); err != nil {
		file.Close()
		return nil, nil, err
	} else if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, statuslineRefusalError(name, false)
	} else {
		return file, info, nil
	}
}

// installStatuslineScript writes body to a fresh regular file and makes it
// executable. The open uses O_NOFOLLOW so a planted link at the destination
// can never redirect the install.
func installStatuslineScript(path string, body []byte) error {
	out, err := openStatuslineFileNoFollow(path, true)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.Write(body); err != nil {
		return err
	}
	return out.Chmod(0o700)
}

// statuslineRefusalError reports a refused staging entry by name, never by
// path: "link simbólico recusado: x" for a link, else "não é um arquivo
// regular".
func statuslineRefusalError(name string, symlink bool) error {
	if symlink {
		return fmt.Errorf("link simbólico recusado: %s", filepath.Base(name))
	}
	return fmt.Errorf("%s não é um arquivo regular", filepath.Base(name))
}

// openVerifiedStagingRoot opens the staging dir handle-anchored and
// confirms it is the same real directory guarded at turn start: a symlink,
// a non-directory, or a different directory all refuse. A planted link is
// removed (the link itself, never its target); anything else is left for
// the user to inspect. Every later read and removal goes through the
// returned root, never through a re-resolved path.
func openVerifiedStagingRoot(stagingDir string, guard os.FileInfo) (*os.Root, error) {
	if info, err := os.Lstat(stagingDir); err != nil {
		return nil, fmt.Errorf("staging ilegível: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		os.Remove(stagingDir)
		return nil, fmt.Errorf("staging trocado por link: link removido, proposta recusada")
	} else if !info.IsDir() {
		return nil, fmt.Errorf("staging inválido: não é um diretório")
	}
	root, err := os.OpenRoot(stagingDir)
	if err != nil {
		return nil, fmt.Errorf("staging ilegível: %v", err)
	}
	dot, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("staging ilegível: %v", err)
	}
	if guard != nil && !os.SameFile(guard, dot) {
		root.Close()
		return nil, fmt.Errorf("staging trocado no meio do turno: proposta recusada")
	}
	return root, nil
}

// removeRootStagingFile removes one known staging entry through the
// verified root, only when it is a plain file: a link (or anything else)
// is never followed, and the anchored remove can never escape the dir.
func removeRootStagingFile(root *os.Root, name string) {
	if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
		return
	}
	if info, err := root.Lstat(name); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return
	}
	root.Remove(name)
}

// discardStatuslineStaging removes this setup's staging files: the proposal
// plus the staged script when named, plus the private dry-run copy. The
// copy lives outside staging and goes first, without depending on the
// staging state; staging files go through a freshly verified root — never
// a sweep, never through a link.
func discardStatuslineStaging(stagingDir string, guard os.FileInfo, scriptStaged, scriptCopy string) {
	removeStatuslineDryRunCopy(scriptCopy)
	root, err := openVerifiedStagingRoot(stagingDir, guard)
	if err != nil {
		return
	}
	defer root.Close()
	removeRootStagingFile(root, "proposal.json")
	if scriptStaged != "" {
		if clean, err := confinedStagingPath(stagingDir, filepath.Base(scriptStaged)); err == nil && clean == scriptStaged {
			removeRootStagingFile(root, filepath.Base(scriptStaged))
		}
	}
}

// statuslineDryRunDirName is the reserved subdir of the scripts dir holding
// private dry-run copies. Only this dir is ever swept for stale copies, so
// an installed script can never share the swept name space.
const statuslineDryRunDirName = ".dryrun"

// removeStatuslineDryRunCopy removes a private dry-run copy by its exact
// reserved path: inside statusline/.dryrun, dryrun-* name. Anything else is
// left alone. The reserved dir itself is dropped while empty, so no trace
// of the copy survives the turn.
func removeStatuslineDryRunCopy(scriptCopy string) {
	if scriptCopy == "" || filepath.Base(filepath.Dir(scriptCopy)) != statuslineDryRunDirName ||
		!strings.HasPrefix(filepath.Base(scriptCopy), "dryrun-") {
		return
	}
	if info, err := os.Lstat(scriptCopy); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		os.Remove(scriptCopy)
	}
	os.Remove(filepath.Dir(scriptCopy))
}

// writeStatuslineDryRunCopy stages the verified script bytes as a private
// executable outside the (swappable) staging tree: statusline/.dryrun/,
// dir 0700, file chmodded through its own descriptor. The dry run executes
// this copy, and apply installs the same bytes from memory. The reserved
// dir follows the staging recipe: a planted link is removed and replaced
// with a real dir, anything else that is not a real dir refuses, and the
// opened root must be the same file the Lstat saw. Sweep and create go
// only through the root, so neither can be led outside it.
func writeStatuslineDryRunCopy(userConfigPath string, body []byte) (string, error) {
	finalDir, err := statuslineFinalDir(userConfigPath)
	if err != nil {
		return "", err
	}
	dryDir := filepath.Join(finalDir, statuslineDryRunDirName)
	if info, err := os.Lstat(dryDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			os.Remove(dryDir)
		} else if !info.IsDir() {
			return "", fmt.Errorf("não foi possível preparar a pasta de scripts: pasta de dry run inválida")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("não foi possível preparar a pasta de scripts: %v", err)
	}
	if err := os.MkdirAll(dryDir, 0o700); err != nil {
		return "", fmt.Errorf("não foi possível preparar a pasta de scripts: %v", err)
	}
	info, err := os.Lstat(dryDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("não foi possível preparar a pasta de scripts: pasta de dry run inválida")
	}
	root, err := os.OpenRoot(dryDir)
	if err != nil {
		return "", fmt.Errorf("não foi possível preparar a pasta de scripts: %v", err)
	}
	defer root.Close()
	if dot, err := root.Stat("."); err != nil || !os.SameFile(info, dot) {
		return "", fmt.Errorf("não foi possível preparar a pasta de scripts: pasta de dry run trocada")
	}
	// Crash leftovers from an interrupted dry run live only in the
	// reserved dir; drop them through the root before minting a fresh copy.
	if dir, err := root.Open("."); err == nil {
		if entries, err := dir.ReadDir(-1); err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if !strings.HasPrefix(name, "dryrun-") {
					continue
				}
				if fi, err := root.Lstat(name); err == nil && fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
					root.Remove(name)
				}
			}
		}
		dir.Close()
	}
	// O_EXCL mints the copy atomically: no follow, no truncation race.
	var copyFile *os.File
	var copyName string
	for i := 0; i < 10; i++ {
		copyName = fmt.Sprintf("dryrun-%d-%d.sh", os.Getpid(), time.Now().UnixNano()+int64(i))
		if f, err := root.OpenFile(copyName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			copyFile = f
			break
		}
	}
	if copyFile == nil {
		return "", fmt.Errorf("não foi possível preparar o script: nome reservado em uso")
	}
	copyPath := filepath.Join(dryDir, copyName)
	if _, err := copyFile.Write(body); err != nil {
		copyFile.Close()
		root.Remove(copyName)
		return "", fmt.Errorf("não foi possível preparar o script: %v", err)
	}
	// Chmod through the open descriptor: the path can never resolve to
	// anything but this file.
	if err := copyFile.Chmod(0o700); err != nil {
		copyFile.Close()
		root.Remove(copyName)
		return "", fmt.Errorf("não foi possível preparar o script: %v", err)
	}
	if err := copyFile.Close(); err != nil {
		root.Remove(copyName)
		return "", fmt.Errorf("não foi possível preparar o script: %v", err)
	}
	return copyPath, nil
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
		discardStatuslineStaging(pending.stagingDir, pending.stagingGuard, pending.scriptStaged, pending.scriptCopy)
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
	// The preview shows the staged dry-run output; a fields-only proposal
	// keeps the live command, so it previews through the same
	// statusLineCustom with the cached output instead of promising field
	// lines the footer will hide for the script.
	render.statuslineCmdOutput = ""
	render.statuslineCmdErr = ""
	if len(scriptLines) > 0 {
		render.statuslineCmdOutput = strings.Join(scriptLines, "\n")
	} else if pending.cfg.Command != nil {
		render.statuslineCmdOutput = m.statuslineCmdOutput
		render.statuslineCmdErr = m.statuslineCmdErr
	}
	width := maxInt(m.width, 80)
	var lines []string
	lines = append(lines, "Prévia com os dados desta sessão:")
	lines = append(lines, render.statusLine(width))
	lines = append(lines, "")
	if pending.cfg.Command == nil && m.statuslineCfg.Command != nil {
		lines = append(lines, "comando: removido")
	}
	// The live command has printed nothing yet, so the preview still shows
	// all three field lines; warn that the last one gives way as soon as
	// the script prints.
	if len(scriptLines) == 0 && pending.cfg.Command != nil &&
		strings.TrimSpace(m.statuslineCmdOutput) == "" && strings.TrimSpace(m.statuslineCmdErr) == "" &&
		len(pending.cfg.Lines) >= maxStatusLineRenderLines {
		lines = append(lines, "aviso: a última linha de campos cede quando o script imprimir")
	}
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
		discardStatuslineStaging(pending.stagingDir, pending.stagingGuard, pending.scriptStaged, pending.scriptCopy)
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
// The cadence always restarts here with a fresh generation: the new
// layout/refresh takes effect on the next Update instead of waiting out the
// old tick, and the old tick is discarded on arrival.
func (m model) applyStatuslinePending() (model, tea.Cmd) {
	pending := m.statuslinePending
	if pending == nil {
		return m, nil
	}
	cfg := pending.cfg
	if pending.scriptStaged != "" {
		// Install the verified bytes from memory: the staging dir is never
		// re-read here, so a swap between the dry run and Enter cannot
		// substitute content. Without verified bytes there is nothing safe
		// to install.
		if len(pending.scriptBody) == 0 {
			return m.appendStatuslineNotice("Statusline\nscript sem bytes verificados: turno descartado"), nil
		}
		finalDir, err := statuslineFinalDir(m.userConfigPath)
		if err != nil {
			return m.appendStatuslineNotice("Statusline\n" + err.Error()), nil
		}
		if err := os.MkdirAll(finalDir, 0o700); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível preparar a pasta de scripts: " + err.Error()), nil
		}
		finalPath := filepath.Join(finalDir, filepath.Base(pending.scriptStaged))
		if info, err := os.Lstat(finalPath); err == nil && !info.Mode().IsRegular() {
			return m.appendStatuslineNotice("Statusline\nnão foi possível instalar o script: destino não é arquivo regular"), nil
		}
		if err := installStatuslineScript(finalPath, pending.scriptBody); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível instalar o script: " + err.Error()), nil
		}
		cfg.Command = &config.StatusLineCommand{Command: statuslineInvokeScript(finalPath)}
	}
	if strings.TrimSpace(m.userConfigPath) != "" {
		if _, err := config.SetStatusLine(m.userConfigPath, cfg); err != nil {
			return m.appendStatuslineNotice("Statusline\nnão foi possível salvar: " + err.Error()), nil
		}
	}
	discardStatuslineStaging(pending.stagingDir, pending.stagingGuard, pending.scriptStaged, pending.scriptCopy)
	m.statuslinePending = nil
	m.statuslineCfg = cfg
	// Drop anything the old config had in flight and restart the cadence, so
	// the new layout/refresh takes effect on the next Update, not whenever
	// the old window happened to elapse.
	m.statuslineCmdOutput = ""
	m.statuslineCmdErr = ""
	m = m.cancelStatuslineRun()
	m.statuslineCmdStartedAt = time.Time{}
	m.statuslineCmdSeq++
	m.statuslineTickGen++
	m = m.appendStatuslineNotice("Statusline\natualizada.")
	if m.statuslineCommandActive() {
		return m, statuslineInitCmd(m.statuslineTickGen)
	}
	return m, nil
}
