package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/workspacetrust"
)

// runTrust implements `qg trust`, letting the user opt a workspace into running
// its project-scoped executable config (hooks, plugins, MCP servers) and start
// language servers for it (internal/lsp gates every server start). Trust is
// keyed on the exact normalized working directory, matching the exact-match trust
// model and cwd-relative project-config discovery.
//
//	qg trust                trust the current working directory
//	qg trust list           print the trusted roots, one per line
//	qg trust remove [path]  untrust the current directory, or a named path
func runTrust(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	if len(args) == 0 {
		return trustCurrentDir(stdout, stderr, deps)
	}
	switch args[0] {
	case "list":
		return trustList(args[1:], stdout, stderr)
	case "remove", "rm", "untrust":
		return trustRemove(args[1:], stdout, stderr, deps)
	case "-h", "--help", "help":
		// Explicit help is a success path: write usage to stdout and exit 0, matching
		// the other subcommands (mcp, sandbox, skills, cron, ...). Only the unknown-
		// subcommand error path below writes usage to stderr with a usage exit code.
		writeTrustUsage(stdout)
		return exitSuccess
	default:
		if _, err := fmt.Fprintf(stderr, "qg trust: unknown subcommand %q\n\n", args[0]); err != nil {
			return exitCrash
		}
		writeTrustUsage(stderr)
		return exitUsage
	}
}

// trustCurrentDir trusts the exact current working directory.
func trustCurrentDir(stdout io.Writer, stderr io.Writer, deps appDeps) int {
	cwd, err := deps.getwd()
	if err != nil {
		// Flatten with %s: ErrorMessage stringifies inline, so %w would be a no-op.
		return writeAppError(stderr, redaction.ErrorMessage(fmt.Errorf("resolve workspace: %s", err), redaction.Options{}), exitCrash)
	}
	if err := workspacetrust.Trust(cwd); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if _, err := fmt.Fprintf(stdout, "Pasta de trabalho confiável: %s\n", cwd); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// trustList prints each trusted root, one per line, or a friendly line when none
// are trusted. With --json it prints {"roots": [...]} ([] when empty).
func trustList(args []string, stdout io.Writer, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "-h", "--help":
			writeTrustUsage(stdout)
			return exitSuccess
		default:
			if strings.HasPrefix(a, "-") {
				return writeExecUsageError(stderr, fmt.Sprintf("flag desconhecida em trust list: %q", a))
			}
			return writeExecUsageError(stderr, fmt.Sprintf("argumento inesperado em trust list: %q", a))
		}
	}
	roots, err := workspacetrust.List()
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if asJSON {
		if roots == nil {
			roots = []string{}
		}
		if err := writePrettyJSON(stdout, map[string]any{"roots": roots}); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if len(roots) == 0 {
		if _, err := fmt.Fprintln(stdout, "Nenhuma pasta de trabalho confiável."); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	for _, root := range roots {
		if _, err := fmt.Fprintln(stdout, root); err != nil {
			return exitCrash
		}
	}
	return exitSuccess
}

// trustRemove untrusts the current directory, or a named path when one is given,
// accepting --json in any position. The JSON payload echoes the normalized path
// (the same entry `trust list` shows) and whether it was actually listed. The
// human text keeps echoing the argument as given.
func trustRemove(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			writeTrustUsage(stdout)
			return exitSuccess
		}
	}
	asJSON := false
	var positional []string
	for _, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			return writeExecUsageError(stderr, fmt.Sprintf("flag desconhecida em trust remove: %q", a))
		default:
			positional = append(positional, a)
		}
	}
	var target string
	switch len(positional) {
	case 0:
		cwd, err := deps.getwd()
		if err != nil {
			// Flatten with %s: ErrorMessage stringifies inline, so %w would be a no-op.
			return writeAppError(stderr, redaction.ErrorMessage(fmt.Errorf("resolve workspace: %s", err), redaction.Options{}), exitCrash)
		}
		target = cwd
	case 1:
		target = positional[0]
	default:
		if _, err := fmt.Fprintln(stderr, "usage: qg trust remove [path]"); err != nil {
			return exitCrash
		}
		return exitUsage
	}
	normalized, err := workspacetrust.NormalizedPath(target)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	wasTrusted, err := workspacetrust.IsTrusted(normalized)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if err := workspacetrust.Untrust(normalized); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if asJSON {
		if err := writePrettyJSON(stdout, map[string]any{"path": normalized, "removed": wasTrusted}); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintf(stdout, "Pasta de trabalho removida da lista de confiáveis: %s\n", target); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func writeTrustUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Uso:
  qg trust                Confia na pasta de trabalho atual
  qg trust list [--json]  Lista as raízes de pasta confiáveis
  qg trust remove [--json] [path]
                          Desconfia da pasta atual, ou de um caminho dado

Confiar deixa o QG rodar hooks, plugins e servidores MCP do projeto da pasta
(./.qg/hooks.json, ./.qg/plugins/, config MCP do projeto) e iniciar
language servers para ela (rodam fora do ambiente isolado e podem
executar código do projeto ao indexar). A confiança é exata por pasta.
`)
}
