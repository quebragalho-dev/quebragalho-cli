package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

type skillListOptions struct {
	json bool
}

func runSkills(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	command := "list"
	rest := args
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			if err := writeSkillsHelp(stdout); err != nil {
				return exitCrash
			}
			return exitSuccess
		case "list", "add", "info", "remove", "rm":
			command, rest = args[0], args[1:]
		default:
			// Treat a leading flag (e.g. --json) as belonging to the implicit
			// `list` command so `qg skills --json` works like `qg plugins`.
			if !strings.HasPrefix(args[0], "-") {
				return writeExecUsageError(stderr, fmt.Sprintf("subcomando skills desconhecido %q", args[0]))
			}
		}
	}

	switch command {
	case "list":
		options, help, err := parseSkillListArgs(rest)
		if err != nil {
			return writeExecUsageError(stderr, err.Error())
		}
		if help {
			if err := writeSkillsListHelp(stdout); err != nil {
				return exitCrash
			}
			return exitSuccess
		}
		return runSkillsList(deps.skillsDir(), options, stdout, stderr)
	case "add":
		return runSkillAdd(rest, deps.skillsDir(), stdout, stderr)
	case "info":
		return runSkillInfo(rest, deps.skillsDir(), stdout, stderr)
	case "remove", "rm":
		return runSkillRemove(rest, deps.skillsDir(), stdout, stderr)
	default:
		return writeExecUsageError(stderr, fmt.Sprintf("subcomando skills desconhecido %q", command))
	}
}

func runSkillsList(dir string, options skillListOptions, stdout io.Writer, stderr io.Writer) int {
	// Management CLI lists global roots only (primary + ~/.agents/skills); plugin
	// skills stay out of `qg skills list` and surface via the agent skill tool.
	roots := skills.GlobalRoots(dir)
	discovered, dups, err := skills.ListFromRoots(roots)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	// Surface name collisions that ListFromRoots silently resolved (earlier root
	// wins), so a shadowed same-named skill is reported instead of just
	// disappearing. Warnings go to stderr, keeping stdout (including --json) clean.
	for _, dup := range dups {
		fmt.Fprintf(stderr, "aviso: skill duplicada %q: usando %s, ignorando %s\n",
			redaction.RedactString(dup.Name, redaction.Options{}),
			redaction.RedactString(dup.Winner, redaction.Options{}),
			redaction.RedactString(dup.Loser, redaction.Options{}))
	}
	if options.json {
		payload := struct {
			Skills []skills.Skill `json:"skills"`
		}{Skills: discovered}
		if err := writePrettyJSON(stdout, redaction.RedactValue(payload, redaction.Options{})); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	output := redaction.RedactString(formatSkillList(discovered), redaction.Options{})
	if _, err := fmt.Fprintln(stdout, output); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func formatSkillList(discovered []skills.Skill) string {
	if len(discovered) == 0 {
		return "Nenhuma skill encontrada."
	}
	lines := []string{"Skills QG:"}
	for _, skill := range discovered {
		line := "  " + skill.Name
		if skill.Description != "" {
			line += " - " + skill.Description
		}
		lines = append(lines, line)
		lines = append(lines, "    "+skill.Path)
	}
	return strings.Join(lines, "\n")
}

func parseSkillListArgs(args []string) (skillListOptions, bool, error) {
	options := skillListOptions{}
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return options, true, nil
		case "--json":
			options.json = true
		default:
			return options, false, execUsageError{fmt.Sprintf("flag skills list desconhecida %q", arg)}
		}
	}
	return options, false, nil
}

func writeSkillsHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg skills <command>

Comandos:
  list                 Lista as skills encontradas (pasta QG + ~/.agents/skills)
  add <git-url|path>   Instala skill na pasta do QG (fixada
                       por checksum em skills.lock)
  info <name>          Mostra frontmatter, origem e hash fixado da skill
  remove <name>        Remove skill e sua entrada do lockfile do QG

list e info também pesquisam ~/.agents/skills quando existe (somente leitura).
add e remove sempre usam a pasta de skills específica do QG.
`)
	return err
}

func writeSkillsListHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg skills list [flags]

Opções:
      --json    Imprime as skills encontradas em JSON
  -h, --help    Mostra esta ajuda
`)
	return err
}
