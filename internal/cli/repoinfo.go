package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/repoinfo"
)

type repoInfoOptions struct {
	json bool
	cwd  string
}

func runRepoInfo(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	options, help, err := parseRepoInfoArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitUsage
	}
	if help {
		writeRepoInfoHelp(stdout)
		return exitSuccess
	}
	workspaceRoot, err := resolveWorkspaceRoot(options.cwd, deps)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitUsage
	}
	now := time.Now
	if deps.now != nil {
		now = deps.now
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := repoinfo.Collect(ctx, repoinfo.Options{Cwd: workspaceRoot, Now: now()})
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitCrash
	}
	if options.json {
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return exitCrash
		}
		fmt.Fprintln(stdout, string(data))
		return exitSuccess
	}
	fmt.Fprint(stdout, formatRepoInfo(info))
	return exitSuccess
}

func parseRepoInfoArgs(args []string) (repoInfoOptions, bool, error) {
	var options repoInfoOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return options, true, nil
		case arg == "--json":
			options.json = true
		case arg == "-C" || arg == "--cwd":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			options.cwd = value
			index = next
		case strings.HasPrefix(arg, "--cwd="):
			options.cwd = strings.TrimSpace(strings.TrimPrefix(arg, "--cwd="))
		default:
			return options, false, execUsageError{fmt.Sprintf("Flag repo-info desconhecida: %s", arg)}
		}
	}
	return options, false, nil
}

const repoInfoTopLanguages = 8

// repoInfoLabelWidth aligns every row label to the same column. Verified
// against real `qg repo-info` output (see TestFormatRepoInfoText).
const repoInfoLabelWidth = 21

func repoInfoRow(label, value string) string {
	return fmt.Sprintf("%-*s %s\n", repoInfoLabelWidth, label+":", value)
}

func repoInfoFileCountLabel(n int) string {
	if n == 1 {
		return "1 arquivo"
	}
	return fmt.Sprintf("%d arquivos", n)
}

func repoInfoCommitLabel(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

func formatRepoInfo(info repoinfo.Info) string {
	var b strings.Builder
	b.WriteString(repoInfoRow("Arquivos", fmt.Sprint(info.FileCount)))
	b.WriteString(repoInfoRow("Pastas", fmt.Sprintf("%d (profundidade máx. %d)", info.DirectoryCount, info.MaxDepth)))
	b.WriteString(repoInfoRow("LOC estimada", fmt.Sprint(info.LOCEstimate)))
	if info.CommitCount != nil {
		b.WriteString(repoInfoRow("Commits", fmt.Sprint(*info.CommitCount)))
	}
	if info.BranchCount != nil {
		b.WriteString(repoInfoRow("Branches", fmt.Sprint(*info.BranchCount)))
	}
	if info.TagCount != nil {
		b.WriteString(repoInfoRow("Tags", fmt.Sprint(*info.TagCount)))
	}
	if info.PrimaryLanguage != "" {
		b.WriteString(repoInfoRow("Linguagem principal", info.PrimaryLanguage))
	}
	if len(info.Languages) > 0 {
		b.WriteString("Linguagens:\n")
		shown := info.Languages
		extra := 0
		if len(shown) > repoInfoTopLanguages {
			extra = len(shown) - repoInfoTopLanguages
			shown = shown[:repoInfoTopLanguages]
		}
		for _, lang := range shown {
			fmt.Fprintf(&b, "  %-14s ~%d LOC  (%s)\n", lang.Name, lang.LOCEstimate, repoInfoFileCountLabel(lang.FileCount))
		}
		if extra > 0 {
			fmt.Fprintf(&b, "  +%d outras\n", extra)
		}
	}
	workspaceType := info.WorkspaceType
	if workspaceType == "none" {
		workspaceType = "nenhuma"
	}
	workspaceValue := workspaceType
	if info.WorkspacePackageCount > 0 {
		workspaceValue += fmt.Sprintf(" (%d pacotes)", info.WorkspacePackageCount)
	}
	b.WriteString(repoInfoRow("Pasta de trabalho", workspaceValue))
	writeRepoInfoList(&b, "Ferramentas de build", info.BuildTools)
	writeRepoInfoList(&b, "Ferramentas de teste", info.TestTools)
	writeRepoInfoList(&b, "CI/CD", info.CICD)
	if info.Branch != "" {
		b.WriteString(repoInfoRow("Branch", info.Branch))
	}
	if info.RemoteURL != "" {
		b.WriteString(repoInfoRow("Remoto", info.RemoteURL))
	}
	if info.AgeDays != nil {
		b.WriteString(repoInfoRow("Idade", fmt.Sprintf("%d dias", *info.AgeDays)))
	}
	if info.Contributors90d != nil {
		b.WriteString(repoInfoRow("Contribuidores", fmt.Sprintf("%d (últimos 90d)", *info.Contributors90d)))
	}
	if info.CommitVelocity30d != nil {
		b.WriteString(repoInfoRow("Ritmo", fmt.Sprintf("%s (últimos 30d)", repoInfoCommitLabel(*info.CommitVelocity30d))))
	}
	return b.String()
}

func writeRepoInfoList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	sorted := append([]string{}, items...)
	sort.Strings(sorted)
	fmt.Fprintf(b, "%-*s %s\n", repoInfoLabelWidth, label+":", strings.Join(sorted, ", "))
}

func writeRepoInfoHelp(w io.Writer) {
	fmt.Fprint(w, `qg repo-info — characterize the current repository (local git only)

Usage:
  qg repo-info [--json] [--cwd <dir>]

Flags:
  --json        Emit the full characterization as JSON.
  -C, --cwd     Run against a different directory.
`)
}
