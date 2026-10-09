package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qggit"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

type usageOptions struct {
	json      bool
	days      int
	since     string
	sessionID string
}

// usageEventSet keeps the persisted events paired with the session metadata they
// were read alongside, so cost reconstruction can resolve each event's owning
// model id during aggregation.
type usageEventSet struct {
	events  []sessions.Event
	meta    []sessions.Metadata
	skipped int
}

// collectUsageData reads every persisted session's events (optionally limited to
// a single session id) and returns them flattened alongside the matching session
// metadata. It mirrors runSearch's persisted-event traversal over the injected
// session store. Sessions whose event log cannot be read are skipped (and
// counted) so one corrupt session can't abort the whole report.
func collectUsageData(store *sessions.Store, sessionFilter string) (usageEventSet, error) {
	metadata, err := store.List()
	if err != nil {
		return usageEventSet{}, err
	}
	set := usageEventSet{}
	for _, meta := range metadata {
		if sessionFilter != "" && meta.SessionID != sessionFilter {
			continue
		}
		sessionEvents, err := store.ReadEvents(meta.SessionID)
		if err != nil {
			set.skipped++
			continue
		}
		set.meta = append(set.meta, meta)
		set.events = append(set.events, sessionEvents...)
	}
	return set, nil
}

// filterEventsSince drops events whose UTC calendar date precedes the inclusive
// lower bound. An empty since returns the events unchanged.
func filterEventsSince(events []sessions.Event, since string) []sessions.Event {
	if since == "" {
		return events
	}
	filtered := make([]sessions.Event, 0, len(events))
	for _, event := range events {
		if eventUTCDate(event.CreatedAt) >= since {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// eventUTCDate maps an RFC3339 timestamp to its UTC calendar date (YYYY-MM-DD)
// so the --since/--days cutoff compares against the same UTC day the report
// buckets under. On a parse failure it falls back to the leading-10 slice.
func eventUTCDate(createdAt string) string {
	if parsed, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return parsed.UTC().Format("2006-01-02")
	}
	if len(createdAt) >= 10 {
		return createdAt[:10]
	}
	return createdAt
}

// runUsage dispatches the two usage surfaces: the explicit `report`
// subcommand reads local session events; every other form (bare `qg usage`
// or window/scope flags) queries the gateway's /v1/usage endpoint.
func runUsage(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		subcommand := strings.ToLower(strings.TrimSpace(args[0]))
		if subcommand == "help" {
			if err := writeUsageHelp(stdout); err != nil {
				return exitCrash
			}
			return exitSuccess
		}
		if subcommand == "report" {
			return runUsageReport(args[1:], stdout, stderr, deps)
		}
		return writeExecUsageError(stderr, fmt.Sprintf("unknown usage command %q (the local report lives under qg usage report)", subcommand))
	}
	return runUsageRemote(args, stdout, stderr, deps)
}

// runUsageRemote queries GET /v1/usage with an optional window and scope.
func runUsageRemote(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	options, help, err := parseRemoteUsageArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeUsageHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if options.scope != "" && options.scope != "user" && options.scope != "key" {
		return writeExecUsageError(stderr, fmt.Sprintf("invalid --scope %q: want user or key", options.scope))
	}
	from, err := parseRemoteUsageTime("--from", options.from)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	to, err := parseRemoteUsageTime("--to", options.to)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	client, code := resolveQGAccountClient(stderr, deps, false)
	if code != exitSuccess {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	report, err := client.Usage(ctx, qgaccount.UsageParams{From: from, To: to, Scope: options.scope})
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitProvider)
	}
	if options.json {
		if err := writePrettyJSON(stdout, map[string]any{
			"from":                report.From,
			"to":                  report.To,
			"scope":               report.Scope,
			"requests":            report.Requests,
			"input_tokens":        report.InputTokens,
			"output_tokens":       report.OutputTokens,
			"cached_input_tokens": report.CachedInputTokens,
			"total":               report.Total(),
		}); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	_, err = fmt.Fprintf(stdout, "Uso de tokens (gateway)\nJanela:  %s → %s\nEscopo:  %s\nPedidos: %s\nEntrada: %s (inclui cache: %s)\nSaída:   %s\nTotal:   %s\n",
		report.From, report.To, report.Scope,
		formatCount(report.Requests),
		formatCount(report.InputTokens), formatCount(report.CachedInputTokens),
		formatCount(report.OutputTokens),
		formatCount(report.Total()),
	)
	if err != nil {
		return exitCrash
	}
	return exitSuccess
}

// remoteUsageOptions is the flag set for the gateway usage query.
type remoteUsageOptions struct {
	from  string
	to    string
	scope string
	json  bool
}

func parseRemoteUsageArgs(args []string) (remoteUsageOptions, bool, error) {
	options := remoteUsageOptions{}
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; arg {
		case "-h", "--help", "help":
			return options, true, nil
		case "--json":
			options.json = true
		case "--from", "--to", "--scope":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			switch arg {
			case "--from":
				options.from = value
			case "--to":
				options.to = value
			case "--scope":
				options.scope = strings.ToLower(strings.TrimSpace(value))
			}
			index = next
		default:
			if strings.HasPrefix(arg, "--from=") || strings.HasPrefix(arg, "--to=") || strings.HasPrefix(arg, "--scope=") {
				name, _, _ := strings.Cut(arg, "=")
				value, err := requiredInlineFlagValue(arg, name)
				if err != nil {
					return options, false, err
				}
				switch name {
				case "--from":
					options.from = value
				case "--to":
					options.to = value
				case "--scope":
					options.scope = strings.ToLower(strings.TrimSpace(value))
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return options, false, execUsageError{fmt.Sprintf("unknown usage flag %q (local report flags live under qg usage report)", arg)}
			}
			return options, false, execUsageError{fmt.Sprintf("unexpected usage argument %q (local report lives under qg usage report)", arg)}
		}
	}
	return options, false, nil
}

// parseRemoteUsageTime accepts YYYY-MM-DD (UTC midnight) or RFC3339.
func parseRemoteUsageTime(flag, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, execUsageError{fmt.Sprintf("invalid %s %q: want YYYY-MM-DD or RFC3339", flag, value)}
}

func runUsageReport(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	options, help, err := parseUsageArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeUsageHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}

	set, err := collectUsageData(deps.newSessionStore(), options.sessionID)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	since := usageSinceCutoff(options, deps)
	events := filterEventsSince(set.events, since)

	// The net-LOC column is best-effort garnish on a token report: outside a
	// git repository (or on any git failure) it degrades to qg instead of
	// aborting the entire report.
	diff := qggit.DiffStat{}
	if workspaceRoot, err := resolveWorkspaceRoot("", deps); err == nil {
		if summary, err := deps.inspectChanges(context.Background(), qggit.InspectOptions{Cwd: workspaceRoot}); err == nil {
			// The --stat summary line ("N files changed, A insertions(+), B
			// deletions(-)") carries no secret-bearing tokens, so parsing the
			// already-redacted DiffStat returned by qggit.Inspect is safe.
			diff = qggit.ParseDiffStat(summary.DiffStat)
		}
	}

	registry, err := modelregistry.DefaultRegistry()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	report, err := usage.BuildReport(events, set.meta, &registry, diff.NetLOC())
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}

	if options.json {
		if err := writePrettyJSON(stdout, report); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintln(stdout, FormatReport(report, diff.Insertions, diff.Deletions)); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// usageSinceCutoff resolves the inclusive lower-bound date (YYYY-MM-DD). An
// explicit --since wins; otherwise --days N derives a cutoff relative to the
// injected clock. An empty result means "no lower bound".
func usageSinceCutoff(options usageOptions, deps appDeps) string {
	if strings.TrimSpace(options.since) != "" {
		return options.since
	}
	if options.days > 0 {
		cutoff := deps.now().UTC().AddDate(0, 0, -(options.days - 1))
		return cutoff.Format("2006-01-02")
	}
	return ""
}

func parseUsageArgs(args []string) (usageOptions, bool, error) {
	options := usageOptions{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return options, true, nil
		case arg == "--json":
			options.json = true
		case arg == "--days":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			days, err := parsePositiveOrZeroInt(value, "--days")
			if err != nil {
				return options, false, err
			}
			options.days = days
			index = next
		case strings.HasPrefix(arg, "--days="):
			days, err := parsePositiveOrZeroInt(strings.TrimSpace(strings.TrimPrefix(arg, "--days=")), "--days")
			if err != nil {
				return options, false, err
			}
			options.days = days
		case arg == "--since":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			if _, parseErr := time.Parse("2006-01-02", value); parseErr != nil {
				return options, false, execUsageError{fmt.Sprintf("invalid --since %q: expected YYYY-MM-DD", value)}
			}
			options.since = value
			index = next
		case strings.HasPrefix(arg, "--since="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--since="))
			if _, parseErr := time.Parse("2006-01-02", value); parseErr != nil {
				return options, false, execUsageError{fmt.Sprintf("invalid --since %q: expected YYYY-MM-DD", value)}
			}
			options.since = value
		case arg == "--session" || arg == "--session-id":
			value, next, err := nextFlagValue(args, index, arg)
			if err != nil {
				return options, false, err
			}
			if strings.TrimSpace(value) == "" {
				return options, false, execUsageError{fmt.Sprintf("%s requires a value", arg)}
			}
			options.sessionID = strings.TrimSpace(value)
			index = next
		case strings.HasPrefix(arg, "--session="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
			if value == "" {
				return options, false, execUsageError{"--session requires a value"}
			}
			options.sessionID = value
		case strings.HasPrefix(arg, "--session-id="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--session-id="))
			if value == "" {
				return options, false, execUsageError{"--session-id requires a value"}
			}
			options.sessionID = value
		default:
			return options, false, execUsageError{fmt.Sprintf("unknown usage flag %q", arg)}
		}
	}
	return options, false, nil
}

// FormatReport renders a usage Report as a per-day table plus totals and net-LOC
// efficiency ratios. Cost and net-LOC are labeled as estimates: persisted usage
// events carry no model id or cost (cost is reconstructed from session
// metadata), and net-LOC is a working-tree diff proxy. The insertions/deletions
// counts come straight from the parsed working-tree --stat.
func FormatReport(report usage.Report, insertions int, deletions int) string {
	var builder strings.Builder
	builder.WriteString("Usage report (cost is a reconstructed estimate)\n\n")
	fmt.Fprintf(&builder, "%-12s %10s %14s %14s\n", "date", "requests", "tokens", "est. cost")
	for _, bucket := range report.Buckets {
		fmt.Fprintf(&builder, "%-12s %10d %14s %14s\n",
			bucket.Date, bucket.Requests, formatCount(int64(bucket.TotalTokens)), formatUSD(bucket.TotalCost))
	}
	fmt.Fprintf(&builder, "\n%-12s %10d %14s %14s\n",
		"total", report.Total.Requests, formatCount(int64(report.Total.TotalTokens)), formatUSD(report.Total.TotalCost))

	fmt.Fprintf(&builder, "\nnet LOC (estimate): +%d / -%d = %d\n",
		insertions, deletions, report.NetLOC)
	if report.NetLOCPositive {
		fmt.Fprintf(&builder, "tokens per net LOC: %.1f\n", report.TokensPerNetLOC)
		fmt.Fprintf(&builder, "est. cost per net LOC: %s\n", formatUSD(report.CostPerNetLOC))
	} else {
		builder.WriteString("tokens per net LOC: n/a (net LOC <= 0)\n")
		builder.WriteString("est. cost per net LOC: n/a (net LOC <= 0)\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

func formatUSD(value float64) string {
	formatted, err := modelregistry.FormatCostUSD(value)
	if err != nil {
		return "$0.0000"
	}
	return formatted
}

func writeUsageHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Usage:
  qg usage [--from <when> --to <when> --scope user|key] [--json]
  qg usage report [flags]

Bare "qg usage" queries the gateway (GET /v1/usage) for the last 24 hours.
input_tokens already includes cached tokens; total = input + output.

Remote flags:
      --from <YYYY-MM-DD|RFC3339>  Window start (default: gateway's 24h)
      --to <YYYY-MM-DD|RFC3339>    Window end (max span: 31 days)
      --scope <user|key>           Count usage for the whole account or this key
      --json                       Machine-readable report
  -h, --help                       Show this help

"qg usage report" summarizes token usage and reconstructed (estimated) cost
from persisted local QG session usage events, plus a working-tree net-LOC
efficiency estimate.

Report flags:
      --json                 Print JSON report
      --days <number>        Only include the most recent N days
      --since <YYYY-MM-DD>    Only include events on or after this date
      --session <id>         Limit the report to one session
  -h, --help                 Show this help

Cost is reconstructed from session model metadata and is an estimate; net LOC
is a working-tree diff proxy and is labeled as an estimate.
`)
	return err
}
