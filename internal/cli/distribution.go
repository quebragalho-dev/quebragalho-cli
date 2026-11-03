package cli

// Distribution command handlers: `qg skill {add,info,remove}`,
// `qg plugin {add,remove}`, and `qg tools {make,list}`. These wire the
// install/scaffold logic in internal/skills, internal/plugins, and
// internal/tools into the CLI. List for skills/plugins reuses the existing
// listing handlers (runSkillsList / runPlugins list).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/plugins"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// distributionAddOptions are the flags shared by `skill add` / `plugin add`.
type distributionAddOptions struct {
	force bool
	json  bool
}

// parseDistributionAddArgs splits a positional source from the add flags.
func parseDistributionAddArgs(args []string, label string) (string, distributionAddOptions, bool, error) {
	options := distributionAddOptions{}
	source := ""
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return "", options, true, nil
		case "--force", "-f":
			options.force = true
		case "--json":
			options.json = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", options, false, execUsageError{fmt.Sprintf("flag %s add desconhecida %q", label, arg)}
			}
			if source != "" {
				return "", options, false, execUsageError{fmt.Sprintf("%s add aceita uma única origem (URL git ou caminho)", label)}
			}
			source = arg
		}
	}
	return source, options, false, nil
}

// --- skill add / info / remove ---

func runSkillAdd(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	source, options, help, err := parseDistributionAddArgs(args, "skill")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeSkillAddHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if source == "" {
		return writeExecUsageError(stderr, "usage: qg skill add <git-url|path> [--force] [--json]")
	}
	if dir == "" {
		return writeAppError(stderr, "não foi possível localizar a pasta de skills", exitCrash)
	}

	result, err := skills.Install(context.Background(), skills.InstallOptions{
		Source: source,
		Dir:    dir,
		Force:  options.force,
	})
	if err != nil {
		if errors.Is(err, skills.ErrNameClash) {
			return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitUsage)
		}
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if options.json {
		if err := writePrettyJSON(stdout, redaction.RedactValue(result, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	lines := []string{}
	if result.Updated {
		lines = append(lines, fmt.Sprintf("Skill %q atualizada.", result.Name))
		lines = append(lines, fmt.Sprintf("  hash: %s -> %s", shortHash(result.PreviousHash), shortHash(result.Hash)))
	} else {
		lines = append(lines, fmt.Sprintf("Skill %q instalada.", result.Name))
		lines = append(lines, "  hash: "+result.Hash)
	}
	lines = append(lines, "  origem: "+result.Source)
	lines = append(lines, "  caminho: "+result.Path)
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(strings.Join(lines, "\n"), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runSkillInfo(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	name, asJSON, help, err := parseNameCommandArgs(args, "skill info")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeSkillInfoHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if name == "" {
		return writeExecUsageError(stderr, "usage: qg skill info <name> [--json]")
	}
	// Resolve across primary + ~/.agents/skills; lock metadata only from primary.
	info, ok := skills.InfoFromRoots(dir, skills.GlobalRoots(dir), name)
	if !ok {
		return writeAppError(stderr, fmt.Sprintf("skill %q não encontrada", name), exitUsage)
	}
	if asJSON {
		if err := writePrettyJSON(stdout, redaction.RedactValue(info, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	lines := []string{info.Skill.Name}
	if info.Skill.Description != "" {
		lines = append(lines, "  "+info.Skill.Description)
	}
	if info.Source != "" {
		lines = append(lines, "  origem: "+info.Source)
	}
	if info.Hash != "" {
		lines = append(lines, "  hash: "+info.Hash)
		drift := "não"
		if info.HashDrift {
			drift = "sim"
		}
		lines = append(lines, "  hash divergente: "+drift)
	}
	lines = append(lines, "  caminho: "+info.Skill.Path)
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(strings.Join(lines, "\n"), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runSkillRemove(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	name, asJSON, help, err := parseNameCommandArgs(args, "skill remove")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeSkillRemoveHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if asJSON {
		return writeExecUsageError(stderr, "skill remove não aceita --json")
	}
	if name == "" {
		return writeExecUsageError(stderr, "usage: qg skill remove <name>")
	}
	if err := skills.Remove(dir, name); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitUsage)
	}
	if _, err := fmt.Fprintf(stdout, "Skill %q removida.\n", name); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// --- plugin add / remove ---

func runPluginAdd(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	source, options, help, err := parseDistributionAddArgs(args, "plugin")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writePluginAddHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if source == "" {
		return writeExecUsageError(stderr, "usage: qg plugin add <git-url|path> [--force] [--json]")
	}
	if dir == "" {
		return writeAppError(stderr, "não foi possível localizar a pasta de plugins", exitCrash)
	}

	result, err := plugins.Install(context.Background(), plugins.InstallOptions{
		Source: source,
		Dir:    dir,
		Force:  options.force,
	})
	if err != nil {
		if errors.Is(err, plugins.ErrNameClash) {
			return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitUsage)
		}
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if options.json {
		if err := writePrettyJSON(stdout, redaction.RedactValue(result, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	lines := []string{}
	if result.Updated {
		lines = append(lines, fmt.Sprintf("Plugin %s@%s atualizado.", result.ID, result.Version))
		lines = append(lines, fmt.Sprintf("  hash: %s -> %s", shortHash(result.PreviousHash), shortHash(result.Hash)))
	} else {
		lines = append(lines, fmt.Sprintf("Plugin %s@%s instalado.", result.ID, result.Version))
		lines = append(lines, "  hash: "+result.Hash)
	}
	lines = append(lines, "  origem: "+result.Source)
	lines = append(lines, "  caminho: "+result.ManifestPath)
	lines = append(lines, "A ativação passa pelo fluxo normal de permissão; nenhum script de instalação foi executado.")
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(strings.Join(lines, "\n"), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runPluginRemove(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	id, asJSON, help, err := parseNameCommandArgs(args, "plugin remove")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writePluginRemoveHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if asJSON {
		return writeExecUsageError(stderr, "plugin remove não aceita --json")
	}
	if id == "" {
		return writeExecUsageError(stderr, "usage: qg plugin remove <id>")
	}
	if err := plugins.Remove(dir, id); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitUsage)
	}
	if _, err := fmt.Fprintf(stdout, "Plugin %q removido.\n", id); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runPluginInfo(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	id, options, help, err := parsePluginInfoArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writePluginInfoHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if id == "" {
		return writeExecUsageError(stderr, "usage: qg plugins info <id> [--user] [--json]")
	}

	cwd, err := deps.getwd()
	if err != nil {
		return writeAppError(stderr, "Não foi possível resolver a pasta de trabalho. Detalhe: "+err.Error(), exitCrash)
	}
	info, err := plugins.Info(plugins.InfoOptions{
		LoadOptions: pluginLoadOptions(deps, cwd, options.user),
	}, id)
	if err != nil {
		if errors.Is(err, plugins.ErrNotInstalled) {
			return writeExecUsageError(stderr, err.Error())
		}
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}

	if options.json {
		if err := writePrettyJSON(stdout, redaction.RedactValue(info, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}

	plugin := info.Plugin
	lines := []string{fmt.Sprintf("%s (%s) %s", plugin.ID, plugin.Name, plugin.Version)}
	state := "ativado"
	if !plugin.Enabled {
		state = "desativado"
	}
	lines = append(lines, "  estado: "+state)
	lines = append(lines, "  origem: "+string(plugin.Source))
	if len(plugin.Tools) > 0 {
		lines = append(lines, fmt.Sprintf("  ferramentas: %d", len(plugin.Tools)))
	}
	if len(plugin.Hooks) > 0 {
		lines = append(lines, fmt.Sprintf("  hooks: %d", len(plugin.Hooks)))
	}
	if len(plugin.Skills) > 0 {
		lines = append(lines, fmt.Sprintf("  skills: %d", len(plugin.Skills)))
	}
	if len(plugin.Prompts) > 0 {
		lines = append(lines, fmt.Sprintf("  prompts: %d", len(plugin.Prompts)))
	}
	if info.LockSource != "" {
		lines = append(lines, "  lock source: "+info.LockSource)
	}
	if info.LockHash != "" {
		lines = append(lines, "  lock hash: "+info.LockHash)
		drift := "não"
		if info.HashDrift {
			drift = "sim"
		}
		lines = append(lines, "  hash divergente: "+drift)
	}
	lines = append(lines, "  manifest: "+plugin.ManifestPath)
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(strings.Join(lines, "\n"), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

type pluginInfoOptions struct {
	json bool
	user bool
}

func parsePluginInfoArgs(args []string) (string, pluginInfoOptions, bool, error) {
	options := pluginInfoOptions{}
	id := ""
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return "", options, true, nil
		case "--json":
			options.json = true
		case "--user":
			options.user = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", options, false, execUsageError{fmt.Sprintf("flag plugins info desconhecida %q", arg)}
			}
			if id != "" {
				return "", options, false, execUsageError{"plugins info aceita um único id"}
			}
			id = arg
		}
	}
	return id, options, false, nil
}

func pluginLoadOptions(deps appDeps, cwd string, userOnly bool) plugins.LoadOptions {
	opts := plugins.LoadOptions{Cwd: cwd, ExcludeProject: userOnly}
	roots := []plugins.Root{{Source: plugins.SourceUser, Path: deps.pluginsDir()}}
	if !userOnly {
		roots = append(roots, plugins.Root{Source: plugins.SourceProject, Path: filepath.Join(cwd, ".qg", "plugins")})
	}
	opts.Roots = roots
	return opts
}

// --- tools make / list ---

func runTools(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	if len(args) == 0 {
		return writeExecUsageError(stderr, "subcomando tools obrigatório. Use `qg tools make <name>` ou `qg tools list`.")
	}
	switch args[0] {
	case "-h", "--help", "help":
		if err := writeToolsHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	case "make", "new", "scaffold":
		return runToolsMake(args[1:], deps.toolsDir(), stdout, stderr)
	case "list", "ls":
		return runToolsList(args[1:], deps.toolsDir(), stdout, stderr)
	default:
		return writeExecUsageError(stderr, fmt.Sprintf("subcomando tools desconhecido %q", args[0]))
	}
}

type toolsMakeOptions struct {
	runtime     string
	description string
	json        bool
}

func runToolsMake(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	name := ""
	options := toolsMakeOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			if err := writeToolsMakeHelp(stdout); err != nil {
				return exitCrash
			}
			return exitSuccess
		case arg == "--json":
			options.json = true
		case arg == "--runtime":
			value, next, err := nextFlagValue(args, i, arg)
			if err != nil {
				return writeExecUsageError(stderr, err.Error())
			}
			options.runtime, i = value, next
		case strings.HasPrefix(arg, "--runtime="):
			options.runtime = strings.TrimSpace(strings.TrimPrefix(arg, "--runtime="))
		case arg == "--description":
			value, next, err := nextFlagValue(args, i, arg)
			if err != nil {
				return writeExecUsageError(stderr, err.Error())
			}
			options.description, i = value, next
		case strings.HasPrefix(arg, "--description="):
			options.description = strings.TrimSpace(strings.TrimPrefix(arg, "--description="))
		case strings.HasPrefix(arg, "-"):
			return writeExecUsageError(stderr, fmt.Sprintf("flag tools make desconhecida %q", arg))
		default:
			if name != "" {
				return writeExecUsageError(stderr, "tools make aceita um único nome de ferramenta")
			}
			name = arg
		}
	}
	if name == "" {
		return writeExecUsageError(stderr, "usage: qg tools make <name> [--runtime shell|node|python] [--description text]")
	}
	if dir == "" {
		return writeAppError(stderr, "não foi possível localizar a pasta de ferramentas", exitCrash)
	}

	result, err := tools.Scaffold(tools.ScaffoldOptions{
		Name:        name,
		Dir:         dir,
		Description: options.description,
		Runtime:     tools.ScaffoldRuntime(strings.TrimSpace(options.runtime)),
	})
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitUsage)
	}
	if options.json {
		if err := writePrettyJSON(stdout, redaction.RedactValue(result, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	lines := []string{
		fmt.Sprintf("Ferramenta %q criada em %s.", result.Name, result.PluginDir),
		"  manifest: " + result.ManifestPath,
		"  entrada:   " + result.EntryPath,
		"",
		"Próximos passos:",
		"  1. Implemente o TODO no script de entrada.",
		"  2. Rode `qg tools list` para confirmar o carregamento.",
		"  3. A ferramenta é ativada pelo fluxo normal de permissão.",
	}
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(strings.Join(lines, "\n"), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runToolsList(args []string, dir string, stdout io.Writer, stderr io.Writer) int {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			if err := writeToolsListHelp(stdout); err != nil {
				return exitCrash
			}
			return exitSuccess
		case "--json":
			asJSON = true
		default:
			return writeExecUsageError(stderr, fmt.Sprintf("flag tools list desconhecida %q", arg))
		}
	}
	if dir == "" {
		return writeAppError(stderr, "não foi possível localizar a pasta de ferramentas", exitCrash)
	}

	// Scaffolded tools are plugins, so list them through the plugin loader scoped
	// to the toolbox dir, then surface their tool extensions.
	result, err := plugins.Load(plugins.LoadOptions{Roots: []plugins.Root{{Source: plugins.SourceUser, Path: dir}}})
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	items := toolboxItems(result.Plugins)
	if asJSON {
		payload := struct {
			Tools       []toolboxItem        `json:"tools"`
			Diagnostics []plugins.Diagnostic `json:"diagnostics"`
		}{Tools: items, Diagnostics: result.Diagnostics}
		if err := writePrettyJSON(stdout, redaction.RedactValue(payload, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintln(stdout, redaction.RedactString(formatToolboxList(items, result.Diagnostics, dir), redaction.Options{})); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// toolboxItem is a flattened view of a plugin tool for `tools list`.
type toolboxItem struct {
	Plugin      string `json:"plugin"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	Permission  string `json:"permission"`
}

func toolboxItems(loaded []plugins.LoadedPlugin) []toolboxItem {
	items := []toolboxItem{}
	for _, plugin := range loaded {
		for _, tool := range plugin.Tools {
			command := tool.Command
			if len(tool.Args) > 0 {
				command = strings.TrimSpace(command + " " + strings.Join(tool.Args, " "))
			}
			items = append(items, toolboxItem{
				Plugin:      plugin.ID,
				Name:        tool.Name,
				Description: tool.Description,
				Command:     command,
				Permission:  string(tool.Permission),
			})
		}
	}
	sort.Slice(items, func(left int, right int) bool {
		if items[left].Plugin != items[right].Plugin {
			return items[left].Plugin < items[right].Plugin
		}
		return items[left].Name < items[right].Name
	})
	return items
}

func formatToolboxList(items []toolboxItem, diagnostics []plugins.Diagnostic, dir string) string {
	lines := []string{}
	if len(items) == 0 {
		lines = append(lines, fmt.Sprintf("Nenhuma ferramenta de plugin QG encontrada em %s.", dir))
	} else {
		lines = append(lines, "Ferramentas QG:")
		for _, item := range items {
			line := "  " + item.Name + " (" + item.Plugin + ")"
			if item.Description != "" {
				line += " - " + item.Description
			}
			lines = append(lines, line)
			lines = append(lines, fmt.Sprintf("    command: %s [%s]", item.Command, item.Permission))
		}
	}
	lines = append(lines, formatToolboxDiagnostics(diagnostics)...)
	return strings.Join(lines, "\n")
}

// formatToolboxDiagnostics renders loader diagnostics the same way the plugins
// listing does, so a broken toolbox plugin surfaces a warning instead of
// silently vanishing from the listing.
func formatToolboxDiagnostics(diagnostics []plugins.Diagnostic) []string {
	if len(diagnostics) == 0 {
		return nil
	}
	lines := []string{"Diagnóstico de ferramentas:"}
	for _, diagnostic := range diagnostics {
		line := fmt.Sprintf("  [%s] %s", diagnostic.Kind, diagnostic.Message)
		lines = append(lines, line)
	}
	return lines
}

// parseNameCommandArgs parses a single positional name plus an optional --json
// flag for the info/remove style commands.
func parseNameCommandArgs(args []string, label string) (string, bool, bool, error) {
	name := ""
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return "", false, true, nil
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false, execUsageError{fmt.Sprintf("flag %s desconhecida %q", label, arg)}
			}
			if name != "" {
				return "", false, false, execUsageError{fmt.Sprintf("%s aceita um único nome", label)}
			}
			name = arg
		}
	}
	return name, asJSON, false, nil
}

// shortHash trims a "sha256:<hex>" fingerprint to a readable prefix for status
// lines while leaving the algorithm tag intact.
func shortHash(hash string) string {
	if hash == "" {
		return "(none)"
	}
	algo, hex, found := strings.Cut(hash, ":")
	if !found {
		if len(hash) > 12 {
			return hash[:12]
		}
		return hash
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return algo + ":" + hex
}

func writeSkillAddHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg skill add <git-url|path> [flags]

Instala uma skill de uma URL git ou caminho local na pasta de skills e
registra um hash do conteúdo em skills.lock. O conteúdo baixado nunca é
executado.

Opções:
  -f, --force    Sobrescreve uma skill instalada de outra origem
      --json     Imprime o resultado da instalação em JSON
  -h, --help     Mostra esta ajuda
`)
	return err
}

func writeSkillInfoHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg skill info <name> [--json]

Mostra o frontmatter da skill mais a origem registrada e o hash fixado.
`)
	return err
}

func writeSkillRemoveHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg skill remove <name>

Remove a pasta de uma skill instalada e sua entrada em skills.lock.
`)
	return err
}

func writePluginAddHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg plugin add <git-url|path> [flags]

Instala um plugin de uma URL git ou caminho local. O manifesto é validado e um
hash do conteúdo é registrado em plugins.lock. Nenhum script de
instalação é executado; o plugin continua ativando pelo fluxo normal de
permissão.

Opções:
  -f, --force    Sobrescreve um plugin instalado de outra origem
      --json     Imprime o resultado da instalação em JSON
  -h, --help     Mostra esta ajuda
`)
	return err
}

func writePluginRemoveHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg plugin remove <id>

Remove a pasta de um plugin instalado e sua entrada em plugins.lock.
`)
	return err
}

func writePluginInfoHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg plugins info <id> [flags]

Opções:
      --user    Só plugins do usuário (ignora ./.qg/plugins)
      --json    Imprime os dados do plugin em JSON
  -h, --help    Mostra esta ajuda
`)
	return err
}

func writeToolsHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg tools <command>

Comandos:
  make <name>    Gera a estrutura inicial de plugin-tool na pasta toolbox
  list           Lista plugin-tools na pasta toolbox
`)
	return err
}

func writeToolsMakeHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg tools make <name> [flags]

Gera a estrutura inicial de plugin-tool (manifesto e stub de entrada) na
pasta toolbox. Após ativar o plugin, a ferramenta fica carregável.

Opções:
      --runtime <shell|node|python>   Linguagem do stub (padrão shell)
      --description <text>            Descrição da ferramenta
      --json                          Imprime o resultado da criação em JSON
  -h, --help                          Mostra esta ajuda
`)
	return err
}

func writeToolsListHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg tools list [flags]

Opções:
      --json    Imprime plugin-tools em JSON
  -h, --help    Mostra esta ajuda
`)
	return err
}
