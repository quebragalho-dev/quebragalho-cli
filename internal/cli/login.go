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
	parsed, err := parseLoginArgs(args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		if err := writeLoginHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if parsed.device {
		return runLoginDevice(stdout, stderr, deps, parsed.noBrowser)
	}
	key := strings.TrimSpace(parsed.apiKey)
	if key == "" {
		key, err = readLoginKeyPrompt(stdout, deps.stdin)
		if err != nil {
			return writeExecUsageError(stderr, err.Error())
		}
	}
	if key == "" {
		return writeExecUsageError(stderr, "Chave de API obrigatória: cole quando for pedido ou passe --api-key")
	}
	if deps.validateLoginKey == nil {
		return writeAppError(stderr, "login validator unavailable", exitCrash)
	}
	ctx, stop := signalContext()
	defer stop()
	if validateErr := deps.validateLoginKey(ctx, key); validateErr != nil {
		msg := redaction.RedactString(validateErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "Não foi possível concluir o login. Detalhe: "+msg, exitProvider)
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
	if _, err := fmt.Fprintf(stdout, "Chave de API validada e salva.\nProvedor %s agora ativo (config: %s).\n", result.Provider.Name, result.ConfigPath); err != nil {
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

// qgDeviceClient returns the injected device-flow client, or the production
// one against the default endpoints.
func qgDeviceClient(deps appDeps) (qgDeviceAPI, error) {
	if deps.newQGDeviceClient != nil {
		return deps.newQGDeviceClient()
	}
	return qgaccount.NewClient(""), nil
}

// runLoginDevice performs the QG device-flow login: the app issues a short
// code, the user approves it at /connect/cli, and the account API key comes
// back on the poll. The key is then validated and stored through the exact
// same path as a pasted key (credstore + apiKeyStored marker).
func runLoginDevice(stdout io.Writer, stderr io.Writer, deps appDeps, noBrowser bool) int {
	client, err := qgDeviceClient(deps)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	ctx, stop := signalContext()
	defer stop()
	auth, err := client.RequestDeviceCode(ctx)
	if err != nil {
		msg := redaction.ErrorMessage(err, redaction.Options{})
		return writeAppError(stderr, "Não foi possível concluir o login via dispositivo. Detalhe: "+msg, exitProvider)
	}
	uri := auth.VerificationURIComplete
	if uri == "" {
		uri = auth.VerificationURI
	}
	if _, err := fmt.Fprintf(stdout, "Aprove o acesso em:\n  %s\nCódigo: %s\n\nAguardando aprovação…\n", uri, auth.UserCode); err != nil {
		return exitCrash
	}
	if !noBrowser && deps.openBrowser != nil {
		// Bounded so a stuck opener cannot stall the login; failure only
		// warns, the printed link still works.
		openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := deps.openBrowser(openCtx, uri); err != nil {
			_, _ = fmt.Fprintf(stderr, "não foi possível abrir o navegador (%v); abra o link acima manualmente\n", err)
		}
		cancel()
	}
	key, err := client.PollDeviceToken(ctx, auth, qgaccount.DevicePollOptions{})
	if err != nil {
		msg := redaction.ErrorMessage(err, redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "Não foi possível concluir o login via dispositivo. Detalhe: "+msg, exitProvider)
	}
	if deps.validateLoginKey == nil {
		return writeAppError(stderr, "login validator unavailable", exitCrash)
	}
	if validateErr := deps.validateLoginKey(ctx, key); validateErr != nil {
		msg := redaction.RedactString(validateErr.Error(), redaction.Options{ExtraSecretValues: []string{key}})
		return writeAppError(stderr, "Não foi possível concluir o login. Detalhe: "+msg, exitProvider)
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
	if _, err := fmt.Fprintf(stdout, "Chave de API validada e salva.\nProvedor %s agora ativo (config: %s).\n", result.Provider.Name, result.ConfigPath); err != nil {
		return exitCrash
	}
	return exitSuccess
}

type loginArgs struct {
	apiKey                  string
	device, noBrowser, help bool
}

// parseLoginArgs accepts -h/--help/help, --api-key in both forms, --device and
// --no-browser; every other flag or positional argument is a usage error.
func parseLoginArgs(args []string) (loginArgs, error) {
	var parsed loginArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			return loginArgs{help: true}, nil
		case arg == "--api-key":
			value, next, err := nextFlagValue(args, i, arg)
			if err != nil {
				return loginArgs{}, err
			}
			parsed.apiKey = value
			i = next
		case strings.HasPrefix(arg, "--api-key="):
			value, err := requiredInlineFlagValue(arg, "--api-key")
			if err != nil {
				return loginArgs{}, err
			}
			parsed.apiKey = value
		case arg == "--device":
			parsed.device = true
		case arg == "--no-browser":
			parsed.noBrowser = true
		case strings.HasPrefix(arg, "-"):
			return loginArgs{}, execUsageError{fmt.Sprintf("unknown flag %q", arg)}
		default:
			return loginArgs{}, execUsageError{fmt.Sprintf("unexpected argument %q", arg)}
		}
	}
	if parsed.device && strings.TrimSpace(parsed.apiKey) != "" {
		return loginArgs{}, execUsageError{"--device and --api-key are mutually exclusive"}
	}
	if parsed.noBrowser && !parsed.device {
		return loginArgs{}, execUsageError{"--no-browser requires --device"}
	}
	return parsed, nil
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
		if _, err := fmt.Fprint(stdout, "Chave de API do Quebra Galho: "); err != nil {
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
	if _, err := fmt.Fprint(stdout, "Chave de API do Quebra Galho: "); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read API key: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func writeLoginHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso:
  qg login [--api-key <key>]
  qg login --device [--no-browser]

Valida uma chave de API do Quebra Galho em GET /v1/models e a salva no
cofre de credenciais (provedor qg, ativado). Sem --api-key, a chave é
pedida no terminal, oculta em um TTY. Se o cofre estiver indisponível,
a chave é gravada em config.json em texto puro e um aviso é mostrado.

--device inicia o fluxo device no navegador: aprove o código mostrado no
link impresso e a chave de API da conta é buscada e salva automaticamente.
O link abre no seu navegador; --no-browser só o imprime (SSH, headless).

Precedência da chave em runtime: uma chave inline no perfil (e
`+"`qg exec --api-key <key>`"+` naquela execução) vale mais que QG_API_KEY (ou
QUEBRAGALHO_API_KEY) do ambiente, que vale mais que a chave salva.
`)
	return err
}
