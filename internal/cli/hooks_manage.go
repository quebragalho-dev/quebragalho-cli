package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/hooks"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// hookConfigStore resolves the writable hook config store for the chosen scope.
// Project scope (the default) targets <cwd>/.qg/hooks.json; --user targets the
// user-level config. The store handles its own locking and atomic writes.
func hookConfigStore(deps appDeps, user bool) (*hooks.ConfigStore, string, error) {
	cwd, err := deps.getwd()
	if err != nil {
		return nil, "", fmt.Errorf("não foi possível resolver a pasta de trabalho: %w", err)
	}
	paths, err := hooks.ResolvePaths(hooks.ResolvePathOptions{Cwd: cwd})
	if err != nil {
		return nil, "", err
	}
	path := paths.ProjectConfigPath
	if user {
		path = paths.UserConfigPath
	}
	store, err := hooks.NewConfigStore(hooks.StoreOptions{ConfigPath: path})
	if err != nil {
		return nil, "", err
	}
	return store, path, nil
}

type hookAddOptions struct {
	json bool
	user bool
	def  hooks.Definition
}

type hookTargetOptions struct {
	json bool
	user bool
}

func runHooksAdd(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	options, help, err := parseHooksAddArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeHooksAddHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	store, path, err := hookConfigStore(deps, options.user)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	saved, err := store.Upsert(options.def)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if options.json {
		payload := struct {
			Hook       hooks.Definition `json:"hook"`
			ConfigPath string           `json:"configPath"`
		}{Hook: saved, ConfigPath: path}
		if err := writePrettyJSON(stdout, redaction.RedactValue(payload, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintf(stdout, "Hook %s [%s] salvo em %s.\n", saved.ID, saved.Event, path); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runHooksRemove(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	options, positional, help, err := parseHooksTargetArgs(args, "remove")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeHooksTargetHelp(stdout, "remove"); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if len(positional) != 1 {
		return writeExecUsageError(stderr, "usage: qg hooks remove <id> [--user] [--json]")
	}
	hookID := positional[0]
	store, path, err := hookConfigStore(deps, options.user)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	removed, err := store.Remove(hookID)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if options.json {
		payload := struct {
			HookID     string `json:"hookId"`
			Removed    bool   `json:"removed"`
			ConfigPath string `json:"configPath"`
		}{HookID: hookID, Removed: removed, ConfigPath: path}
		if err := writePrettyJSON(stdout, payload); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if removed {
		if _, err := fmt.Fprintf(stdout, "Hook %s removido de %s.\n", hookID, path); err != nil {
			return exitCrash
		}
	} else if _, err := fmt.Fprintf(stdout, "Nenhum hook chamado %s configurado em %s.\n", hookID, path); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runHooksToggle(args []string, stdout io.Writer, stderr io.Writer, deps appDeps, disabled bool) int {
	commandName := "enable"
	if disabled {
		commandName = "disable"
	}
	options, positional, help, err := parseHooksTargetArgs(args, commandName)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeHooksTargetHelp(stdout, commandName); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if len(positional) != 1 {
		return writeExecUsageError(stderr, fmt.Sprintf("usage: qg hooks %s <id> [--user] [--json]", commandName))
	}
	hookID := positional[0]
	store, path, err := hookConfigStore(deps, options.user)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	found, err := store.SetEnabled(hookID, !disabled)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if !found {
		return writeExecUsageError(stderr, fmt.Sprintf("nenhum hook chamado %s configurado em %s", hookID, path))
	}
	state := "ativado"
	if disabled {
		state = "desativado"
	}
	if options.json {
		payload := struct {
			HookID     string `json:"hookId"`
			Enabled    bool   `json:"enabled"`
			ConfigPath string `json:"configPath"`
		}{HookID: hookID, Enabled: !disabled, ConfigPath: path}
		if err := writePrettyJSON(stdout, payload); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintf(stdout, "Hook %s agora está %s em %s.\n", hookID, state, path); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func parseHooksTargetArgs(args []string, command string) (hookTargetOptions, []string, bool, error) {
	options := hookTargetOptions{}
	positional := []string{}
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return options, positional, true, nil
		case "--json":
			options.json = true
		case "--user":
			options.user = true
		default:
			if strings.HasPrefix(arg, "-") {
				return options, positional, false, execUsageError{fmt.Sprintf("flag desconhecida em hooks %s: %q", command, arg)}
			}
			positional = append(positional, arg)
		}
	}
	return options, positional, false, nil
}

func parseHooksAddArgs(args []string) (hookAddOptions, bool, error) {
	options := hookAddOptions{}
	// New hooks are enabled; persisted state is managed with enable/disable.
	options.def.Enabled = true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return options, true, nil
		case arg == "--json":
			options.json = true
		case arg == "--user":
			options.user = true
		case arg == "--event":
			value, err := requiredNextMCPFlagValue(args, &i, "--event")
			if err != nil {
				return options, false, err
			}
			options.def.Event = hooks.Event(value)
		case strings.HasPrefix(arg, "--event="):
			value, err := requiredInlineFlagValue(arg, "--event")
			if err != nil {
				return options, false, err
			}
			options.def.Event = hooks.Event(value)
		case arg == "--command":
			value, err := requiredNextMCPFlagValue(args, &i, "--command")
			if err != nil {
				return options, false, err
			}
			options.def.Command = value
		case strings.HasPrefix(arg, "--command="):
			value, err := requiredInlineFlagValue(arg, "--command")
			if err != nil {
				return options, false, err
			}
			options.def.Command = value
		case arg == "--name":
			value, err := requiredNextMCPFlagValue(args, &i, "--name")
			if err != nil {
				return options, false, err
			}
			options.def.Name = value
		case strings.HasPrefix(arg, "--name="):
			value, err := requiredInlineFlagValue(arg, "--name")
			if err != nil {
				return options, false, err
			}
			options.def.Name = value
		case arg == "--description":
			value, err := requiredNextMCPFlagValue(args, &i, "--description")
			if err != nil {
				return options, false, err
			}
			options.def.Description = value
		case strings.HasPrefix(arg, "--description="):
			value, err := requiredInlineFlagValue(arg, "--description")
			if err != nil {
				return options, false, err
			}
			options.def.Description = value
		case arg == "--matcher":
			value, err := requiredNextMCPFlagValue(args, &i, "--matcher")
			if err != nil {
				return options, false, err
			}
			options.def.Matcher = value
		case strings.HasPrefix(arg, "--matcher="):
			value, err := requiredInlineFlagValue(arg, "--matcher")
			if err != nil {
				return options, false, err
			}
			options.def.Matcher = value
		case arg == "--arg":
			value, err := requiredNextMCPFlagValue(args, &i, "--arg")
			if err != nil {
				return options, false, err
			}
			options.def.Args = append(options.def.Args, value)
		case strings.HasPrefix(arg, "--arg="):
			value, err := requiredInlineFlagValue(arg, "--arg")
			if err != nil {
				return options, false, err
			}
			options.def.Args = append(options.def.Args, value)
		case strings.HasPrefix(arg, "-"):
			return options, false, execUsageError{fmt.Sprintf("flag desconhecida em hooks add: %q", arg)}
		case options.def.ID == "":
			options.def.ID = arg
		default:
			return options, false, execUsageError{"usage: qg hooks add <id> --event <event> --command <cmd> [flags]"}
		}
	}

	options.def.ID = strings.TrimSpace(options.def.ID)
	if options.def.ID == "" {
		return options, false, execUsageError{"usage: qg hooks add <id> --event <event> --command <cmd> [flags]"}
	}
	if strings.TrimSpace(string(options.def.Event)) == "" {
		return options, false, execUsageError{"qg hooks add exige --event"}
	}
	if !hooks.IsValidEvent(options.def.Event) {
		return options, false, execUsageError{fmt.Sprintf("valor inválido para --event: %q; valores válidos: beforeTool, afterTool, sessionStart, sessionEnd, specialistStart, specialistStop", options.def.Event)}
	}
	if strings.TrimSpace(options.def.Command) == "" {
		return options, false, execUsageError{"qg hooks add exige --command"}
	}
	if options.def.Args == nil {
		options.def.Args = []string{}
	}
	return options, false, nil
}

func writeHooksAddHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg hooks add <id> --event <event> --command <cmd> [flags]

Eventos:
  beforeTool, afterTool, sessionStart, sessionEnd,
  specialistStart, specialistStop

Opções:
      --event <event>        Evento do hook (obrigatório)
      --command <cmd>        Comando a executar (obrigatório)
      --name <name>          Nome legível do hook
      --description <text>   Descrição do hook
      --matcher <pattern>    Matcher de ferramenta (só beforeTool/afterTool)
      --arg <value>          Argumento do comando (repetível)
      --user                 Grava na config do usuário em vez da do projeto
      --json                 Imprime o resultado do comando em JSON
  -h, --help                 Mostra esta ajuda

Hooks novos já nascem ativos; use `+"`qg hooks disable <id>`"+` para desativar.
`)
	return err
}

func writeHooksTargetHelp(w io.Writer, command string) error {
	_, err := fmt.Fprintf(w, `Uso:
  qg hooks %s <id> [flags]

Opções:
      --user    Usa a config do usuário em vez da do projeto
      --json    Imprime o resultado do comando em JSON
  -h, --help    Mostra esta ajuda
`, command)
	return err
}
