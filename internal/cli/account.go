package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// qgAccountAPI is the account surface the CLI commands consume; tests inject
// a fake, production uses *qgaccount.Client.
type qgAccountAPI interface {
	Balance(ctx context.Context) (qgaccount.Balance, error)
	Key(ctx context.Context) (qgaccount.KeyInfo, error)
	Usage(ctx context.Context, params qgaccount.UsageParams) (qgaccount.UsageReport, error)
	Models(ctx context.Context) ([]qgaccount.ModelInfo, error)
}

// resolveQGAccountClient builds the production account client from the active
// QG profile (inline key → env → credential store), or returns the injected
// test client. quiet discards diagnostics for surfaces that degrade gracefully
// (models enrichment), so a missing key never spills guidance onto stderr.
func resolveQGAccountClient(stderr io.Writer, deps appDeps, quiet bool) (qgAccountAPI, int) {
	if quiet {
		stderr = io.Discard
	}
	if deps.newQGAccountClient != nil {
		client, err := deps.newQGAccountClient()
		if err != nil {
			return nil, writeAppError(stderr, err.Error(), exitProvider)
		}
		return client, exitSuccess
	}
	resolved, code := resolveCommandCenterConfig(stderr, deps)
	if code != exitSuccess {
		return nil, code
	}
	profile, err := findQGProfile(resolved)
	if err != nil {
		return nil, codeForQuiet(quiet, exitProvider, writeExecUsageError(stderr, err.Error()))
	}
	authed := discoveryCredentialProfile(profile)
	if strings.TrimSpace(authed.APIKey) == "" {
		return nil, codeForQuiet(quiet, exitProvider, writeExecUsageError(stderr, "no Quebra Galho API key: run qg login (or export QG_API_KEY)"))
	}
	return qgaccount.NewClient(authed.APIKey), exitSuccess
}

// codeForQuiet returns fallbackExit when quiet (the error write already went to
// io.Discard); otherwise it returns the exit code produced by writing the real
// message. The write call is evaluated eagerly by the caller.
func codeForQuiet(quiet bool, fallbackExit int, writtenExit int) int {
	if quiet {
		return fallbackExit
	}
	return writtenExit
}

// findQGProfile locates the Quebra Galho profile among the resolved ones.
func findQGProfile(resolved config.ResolvedConfig) (config.ProviderProfile, error) {
	for _, profile := range resolved.Providers {
		if profile.CatalogID == providercatalog.QGID || profile.Name == providercatalog.QGID {
			return profile, nil
		}
	}
	if resolved.Provider.CatalogID == providercatalog.QGID || resolved.Provider.Name == providercatalog.QGID {
		return resolved.Provider, nil
	}
	return config.ProviderProfile{}, fmt.Errorf("no Quebra Galho provider configured: run qg login")
}

func runBalance(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	jsonOutput, help, err := parseAccountJSONArgs(args, "balance")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if _, err := fmt.Fprint(stdout, "Usage: qg balance [--json]\n\nShow the live Quebra Galho wallet balance (always a fresh API call).\n\nFlags:\n      --json   Machine-readable output (decimal strings, no rounding)\n"); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	client, code := resolveQGAccountClient(stderr, deps, false)
	if code != exitSuccess {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	balance, err := client.Balance(ctx)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitProvider)
	}
	if jsonOutput {
		if err := writePrettyJSON(stdout, map[string]string{
			"currency":  balance.Currency,
			"available": balance.Available,
			"default":   balance.Default,
			"bonus":     balance.Bonus,
		}); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	available, err := qgaccount.FormatBRL(balance.Available)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	def, err := qgaccount.FormatBRL(balance.Default)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	bonus, err := qgaccount.FormatBRL(balance.Bonus)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	_, err = fmt.Fprintf(stdout, "Saldo disponível: %s\nSaldo padrão:     %s\nBônus:            %s\nMoeda:            %s\n", available, def, bonus, balance.Currency)
	if err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runKey(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	jsonOutput, help, err := parseAccountJSONArgs(args, "key")
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if _, err := fmt.Fprint(stdout, "Usage: qg key [--json]\n\nShow the profile of the active Quebra Galho API key (alias: qg whoami).\nThe key itself is never printed.\n\nFlags:\n      --json   Machine-readable output\n"); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	client, code := resolveQGAccountClient(stderr, deps, false)
	if code != exitSuccess {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	info, err := client.Key(ctx)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitProvider)
	}
	if jsonOutput {
		if err := writePrettyJSON(stdout, info); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	models := "todos"
	if info.AllowedModels != nil {
		models = strings.Join(info.AllowedModels, ", ")
	}
	_, err = fmt.Fprintf(stdout, "Nome:             %s\nPrefixo:          %s\nID:               %s\nCriada em:        %s\nPlano:            %s (%s)\nModelos permitidos: %s\n",
		info.Name, info.KeyPrefix, info.ID, info.CreatedAt, info.Plan.Code, info.Plan.Type, models)
	if err != nil {
		return exitCrash
	}
	return exitSuccess
}

// parseAccountJSONArgs accepts -h/--help/help and --json for the account
// commands.
func parseAccountJSONArgs(args []string, command string) (bool, bool, error) {
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "-h", "--help", "help":
			return false, true, nil
		case "--json":
			jsonOutput = true
		default:
			return false, false, execUsageError{fmt.Sprintf("unknown %s flag %q", command, arg)}
		}
	}
	return jsonOutput, false, nil
}

// formatCount groups an integer with "." thousands separators (pt-BR),
// delegating the digit grouping to qgaccount so counts and BRL amounts use one
// implementation.
func formatCount(value int64) string {
	digits := strconv.FormatInt(value, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign = "-"
		digits = digits[1:]
	}
	return sign + qgaccount.GroupThousands(digits)
}
