package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

// defaultValidateLoginKey builds the production key validator: the key must
// answer GET <gateway>/models.
func defaultValidateLoginKey(ctx context.Context, key string) error {
	descriptor, err := providercatalog.Require(providercatalog.QGID)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	return validateQuebraGalhoKey(ctx, descriptor.DefaultBaseURL, key, client)
}

// validateQuebraGalhoKey checks the key against GET <baseURL>/models with a
// Bearer header. 2xx accepts; 401/403 means the gateway rejected the key;
// anything else (transport, 5xx) is reported as an unexpected response. The
// key never appears in the returned messages.
func validateQuebraGalhoKey(ctx context.Context, baseURL, key string, client *http.Client) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("API key is empty")
	}
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build validation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach the gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the gateway rejected the API key (HTTP %d)", resp.StatusCode)
	}
	return fmt.Errorf("unexpected gateway response (HTTP %d)", resp.StatusCode)
}

// runLogin implements `qg login [--api-key <key>]`: read the key (flag or
// prompt), validate it against GET /v1/models, then persist it through the
// same setup path (`saveSetupProvider`) so the secret lands in the credential
// store with config.json keeping only the apiKeyStored marker.
func runLogin(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	flagKey, device, help, err := parseLoginArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if help {
		if err := writeLoginHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if device {
		return runLoginDevice(stdout, stderr, deps)
	}
	key := strings.TrimSpace(flagKey)
	if key == "" {
		key, err = readLoginKeyPrompt(stdout, deps.stdin)
		if err != nil {
			return writeExecUsageError(stderr, err.Error())
		}
	}
	if key == "" {
		return writeExecUsageError(stderr, "API key required: paste it at the prompt or pass --api-key")
	}
	if deps.validateLoginKey == nil {
		return writeAppError(stderr, "login validator unavailable", exitCrash)
	}
	ctx, stop := signalContext()
	defer stop()
	if validateErr := deps.validateLoginKey(ctx, key); validateErr != nil {
		msg := redaction.RedactString(validateErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "login failed: "+msg, exitProvider)
	}
	result, saveErr := saveSetupProvider(deps, tui.SetupSelection{
		CatalogID: providercatalog.QGID,
		APIKey:    key,
	}, setupSaveOptions{})
	if saveErr != nil {
		msg := redaction.RedactString(saveErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, msg, exitCrash)
	}
	if result.KeyStoreFallback {
		warnKeyStoreFallback(stderr, result.ConfigPath)
	}
	if _, err := fmt.Fprintf(stdout, "API key validated and saved.\nProvider %s is now active (config: %s).\n", result.Provider.Name, result.ConfigPath); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// qgDeviceAPI is the device-flow surface runLoginDevice consumes; tests inject
// a fake, production uses *qgaccount.Client.
type qgDeviceAPI interface {
	RequestDeviceCode(ctx context.Context) (qgaccount.DeviceAuth, error)
	PollDeviceToken(ctx context.Context, auth qgaccount.DeviceAuth, opts qgaccount.DevicePollOptions) (string, error)
}

var _ qgDeviceAPI = (*qgaccount.Client)(nil)

// runLoginDevice performs the QG device-flow login: the app issues a short
// code, the user approves it at /connect/cli, and the account API key comes
// back on the poll. The key is then validated and stored through the exact
// same path as a pasted key (credstore + apiKeyStored marker).
func runLoginDevice(stdout io.Writer, stderr io.Writer, deps appDeps) int {
	var client qgDeviceAPI = qgaccount.NewClient("")
	if deps.newQGDeviceClient != nil {
		built, err := deps.newQGDeviceClient()
		if err != nil {
			return writeAppError(stderr, err.Error(), exitCrash)
		}
		client = built
	}
	ctx, stop := signalContext()
	defer stop()
	auth, err := client.RequestDeviceCode(ctx)
	if err != nil {
		msg := redaction.ErrorMessage(err, redaction.Options{})
		return writeAppError(stderr, "device login failed: "+msg, exitProvider)
	}
	uri := auth.VerificationURIComplete
	if uri == "" {
		uri = auth.VerificationURI
	}
	if _, err := fmt.Fprintf(stdout, "Aprove o acesso em:\n  %s\nCódigo: %s\n\nAguardando aprovação…\n", uri, auth.UserCode); err != nil {
		return exitCrash
	}
	key, err := client.PollDeviceToken(ctx, auth, qgaccount.DevicePollOptions{})
	if err != nil {
		msg := redaction.ErrorMessage(err, redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "device login failed: "+msg, exitProvider)
	}
	if deps.validateLoginKey == nil {
		return writeAppError(stderr, "login validator unavailable", exitCrash)
	}
	if validateErr := deps.validateLoginKey(ctx, key); validateErr != nil {
		msg := redaction.RedactString(validateErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "login failed: "+msg, exitProvider)
	}
	result, saveErr := saveSetupProvider(deps, tui.SetupSelection{
		CatalogID: providercatalog.QGID,
		APIKey:    key,
	}, setupSaveOptions{})
	if saveErr != nil {
		msg := redaction.RedactString(saveErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, msg, exitCrash)
	}
	if result.KeyStoreFallback {
		warnKeyStoreFallback(stderr, result.ConfigPath)
	}
	if _, err := fmt.Fprintf(stdout, "API key validated and saved.\nProvider %s is now active (config: %s).\n", result.Provider.Name, result.ConfigPath); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// parseLoginArgs accepts -h/--help/help and --api-key in both forms; every
// other flag or positional argument is a usage error.
func parseLoginArgs(args []string) (string, bool, bool, error) {
	apiKey := ""
	device := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return "", false, true, nil
		case arg == "--api-key":
			value, next, err := nextFlagValue(args, i, arg)
			if err != nil {
				return "", false, false, err
			}
			apiKey = value
			i = next
		case strings.HasPrefix(arg, "--api-key="):
			value, err := requiredInlineFlagValue(arg, "--api-key")
			if err != nil {
				return "", false, false, err
			}
			apiKey = value
		case arg == "--device":
			device = true
		case strings.HasPrefix(arg, "-"):
			return "", false, false, execUsageError{fmt.Sprintf("unknown flag %q", arg)}
		default:
			return "", false, false, execUsageError{fmt.Sprintf("unexpected argument %q", arg)}
		}
	}
	if device && strings.TrimSpace(apiKey) != "" {
		return "", false, false, execUsageError{"--device and --api-key are mutually exclusive"}
	}
	return apiKey, device, false, nil
}

// readLoginKeyPrompt writes the prompt to stdout and reads one line from
// stdin. On a TTY the input is hidden with term.ReadPassword so the key never
// reaches the screen (the terminal driver must stop echoing it); piped or
// injected readers keep the plain line read, where EOF with a partial line
// still yields that line (input without a trailing newline).
func readLoginKeyPrompt(stdout io.Writer, stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", nil
	}
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		if _, err := fmt.Fprint(stdout, "Quebra Galho API key: "); err != nil {
			return "", err
		}
		hidden, err := term.ReadPassword(int(file.Fd()))
		// ReadPassword swallows the typed newline; emit one so the next output
		// does not continue on the prompt line.
		_, _ = fmt.Fprintln(stdout)
		if err != nil {
			return "", fmt.Errorf("read API key: %w", err)
		}
		return strings.TrimSpace(string(hidden)), nil
	}
	if _, err := fmt.Fprint(stdout, "Quebra Galho API key: "); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read API key: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func writeLoginHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Usage:
  qg login [--api-key <key>]
  qg login --device

Validates a Quebra Galho API key against GET /v1/models and saves it to the
credential store (provider qg, set active). Without --api-key the key is
prompted for, hidden on a TTY. If the credential store is unavailable the
key is written to config.json in plaintext instead and a warning is printed.

--device starts the browser device flow instead: approve the shown code at
the printed link and the account API key is fetched and saved automatically.

Precedence at runtime: QG_API_KEY (or QUEBRAGALHO_API_KEY) from the
environment wins over the saved key, and `+"`qg exec --api-key <key>`"+` wins
over everything for that run.
`)
	return err
}
