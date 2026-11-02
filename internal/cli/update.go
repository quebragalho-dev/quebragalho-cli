package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
)

type updateOptions struct {
	check      bool
	apply      bool
	json       bool
	repository string
	endpoint   string
	timeout    time.Duration
	target     string
}

func runUpdate(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	return runUpdateCommand(args, stdout, stderr, deps, false)
}

func runUpgrade(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	return runUpdateCommand(args, stdout, stderr, deps, true)
}

// runUpdateCommand backs both `qg update` and `qg upgrade`. defaultApply
// makes `--apply` the implicit behavior for `qg upgrade` when neither
// `--check` nor `--apply` is passed explicitly; `qg update` keeps requiring
// one of the two so existing scripts around `qg update --check` don't change.
func runUpdateCommand(args []string, stdout io.Writer, stderr io.Writer, deps appDeps, defaultApply bool) int {
	options, help, err := parseUpdateArgs(args)
	if err != nil {
		return writeUsageError(stderr, err.Error())
	}
	if help {
		if err := writeUpdateHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if options.check && options.apply {
		return writeUsageError(stderr, "Passe apenas uma das opções: --check ou --apply.")
	}
	if !options.check && !options.apply {
		if !defaultApply {
			return writeUsageError(stderr, "Passe --check para verificar atualizações ou --apply para instalar uma (ou use `qg upgrade`).")
		}
		options.apply = true
	}
	if options.apply && options.target != "" {
		return writeUsageError(stderr, "--target não pode ser combinado com --apply; ele só verifica artefatos de release para --check.")
	}
	updateOptions := update.Options{
		CurrentVersion: version,
		Repository:     options.repository,
		Endpoint:       options.endpoint,
		Timeout:        options.timeout,
	}
	if options.target != "" {
		target, err := update.ResolveTarget(options.target)
		if err != nil {
			return writeUsageError(stderr, err.Error())
		}
		updateOptions.GOOS = target.GOOS
		updateOptions.GOARCH = target.GOARCH
	}
	if options.apply {
		result, err := deps.applyUpdate(context.Background(), updateOptions)
		if err != nil {
			return writeAppError(stderr, "Não foi possível instalar a atualização. Detalhe: "+err.Error(), exitCrash)
		}
		if options.json {
			if err := writePrettyJSON(stdout, result); err != nil {
				return exitCrash
			}
			return exitSuccess
		}
		if _, err := fmt.Fprintln(stdout, update.FormatApply(result)); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	result, err := deps.checkUpdate(context.Background(), updateOptions)
	if err != nil {
		return writeAppError(stderr, "Não foi possível verificar atualizações. Detalhe: "+err.Error(), exitCrash)
	}
	if options.json {
		if err := writePrettyJSON(stdout, result); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintln(stdout, update.Format(result)); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func parseUpdateArgs(args []string) (updateOptions, bool, error) {
	options := updateOptions{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return options, true, nil
		case arg == "--check":
			options.check = true
		case arg == "--apply":
			options.apply = true
		case arg == "--json":
			options.json = true
		case arg == "--repo":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			options.repository = strings.TrimSpace(value)
			index = next
		case strings.HasPrefix(arg, "--repo="):
			options.repository = strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
		case arg == "--endpoint":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			options.endpoint = strings.TrimSpace(value)
			index = next
		case strings.HasPrefix(arg, "--endpoint="):
			options.endpoint = strings.TrimSpace(strings.TrimPrefix(arg, "--endpoint="))
		case arg == "--timeout":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			timeout, err := parseUpdateTimeout(value)
			if err != nil {
				return options, false, err
			}
			options.timeout = timeout
			index = next
		case strings.HasPrefix(arg, "--timeout="):
			timeout, err := parseUpdateTimeout(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				return options, false, err
			}
			options.timeout = timeout
		case arg == "--target":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			target, err := parseUpdateTarget(value)
			if err != nil {
				return options, false, err
			}
			options.target = target
			index = next
		case strings.HasPrefix(arg, "--target="):
			target, err := parseUpdateTarget(strings.TrimPrefix(arg, "--target="))
			if err != nil {
				return options, false, err
			}
			options.target = target
		default:
			return options, false, execUsageError{fmt.Sprintf("flag update desconhecida %q", arg)}
		}
	}
	return options, false, nil
}

func parseUpdateTimeout(value string) (time.Duration, error) {
	timeout, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, execUsageError{fmt.Sprintf("timeout de atualização inválido %q: use uma duração como 5s ou 750ms", value)}
	}
	if timeout <= 0 {
		return 0, execUsageError{fmt.Sprintf("timeout de atualização inválido %q: o timeout precisa ser uma duração positiva", value)}
	}
	return timeout, nil
}

func parseUpdateTarget(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", execUsageError{"--target exige um valor não vazio"}
	}
	return target, nil
}

func writeUpdateHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Usage:
  qg update --check [flags]
  qg update --apply [flags]
  qg upgrade [flags]

Flags:
      --check                 Check the latest GitHub release without installing
      --apply                 Download, verify, and install the latest release
      --json                  Print the update result as JSON
      --repo <owner/repo>     Repository to use when no endpoint is provided (--check and --apply; ignored for the install source by an npm-managed apply)
      --endpoint <url|repo>   Release API URL or owner/repo slug to use (--check and --apply; ignored for the install source by an npm-managed apply)
      --timeout <duration>    Release check timeout (default 5s)
      --target <platform>     Release target to verify with --check (for example windows-x64); not valid with --apply
  -h, --help                  Show this help
`)
	return err
}
