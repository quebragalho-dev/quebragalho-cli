package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// authLoginRemovedMessage replaces the removed OAuth login flows (Etapa 4):
// QG authenticates with a plain API key, so browser/device login has no path
// left. logout / status / refresh / reset keep working on pre-existing token
// stores.
const authLoginRemovedMessage = "OAuth login is not supported: run `qg login` to save an API key (" + providercatalog.KeyEnvHint + " also work from the environment), then run `qg setup qg` (see `qg setup --help`)"

// runAuth dispatches `qg auth <command>`. The login subcommands are removed
// (Etapa 4) and report authLoginRemovedMessage; what remains is maintenance of
// pre-existing token stores. MCP server auth (`qg mcp oauth`) is unchanged.
func runAuth(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	if len(args) == 0 {
		return writeExecUsageError(stderr, "auth subcommand required. Use `qg auth status`.")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if err := writeAuthHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	switch args[0] {
	case "login":
		// Keep `--help` answering so a user following stale docs still lands on
		// the current command list instead of a dead end. Removed vendor names
		// (openrouter, chatgpt, …) fall through to the unknown-subcommand path.
		for _, arg := range args[1:] {
			if arg == "-h" || arg == "--help" || arg == "help" {
				if err := writeAuthHelp(stdout); err != nil {
					return exitCrash
				}
				return exitSuccess
			}
		}
		return writeExecUsageError(stderr, authLoginRemovedMessage)
	case "logout":
		return runAuthLogout(args[1:], stdout, stderr, deps)
	case "status":
		return runAuthStatus(args[1:], stdout, stderr, deps)
	case "refresh":
		return runAuthRefresh(args[1:], stdout, stderr, deps)
	case "reset":
		return runAuthReset(args[1:], stdout, stderr, deps)
	default:
		return writeExecUsageError(stderr, fmt.Sprintf("unknown auth subcommand %q", args[0]))
	}
}

// authArgs is the parsed form of an auth subcommand's arguments.
type authArgs struct {
	positional []string
	json       bool
	watch      bool
	confirm    bool
	scopes     []string
	help       bool
}

func parseAuthArgs(sub string, args []string) (authArgs, error) {
	var parsed authArgs
	addScope := func(scope string) error {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return fmt.Errorf("--scope requires a non-empty value")
		}
		parsed.scopes = append(parsed.scopes, scope)
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			parsed.help = true
		case arg == "--json":
			parsed.json = true
		case arg == "--watch":
			parsed.watch = true
		case arg == "--confirm":
			parsed.confirm = true
		case arg == "--scope":
			if i+1 >= len(args) {
				return authArgs{}, fmt.Errorf("--scope requires a value")
			}
			i++
			if err := addScope(args[i]); err != nil {
				return authArgs{}, err
			}
		case strings.HasPrefix(arg, "--scope="):
			if err := addScope(strings.TrimPrefix(arg, "--scope=")); err != nil {
				return authArgs{}, err
			}
		case strings.HasPrefix(arg, "-"):
			return authArgs{}, fmt.Errorf("unknown flag %q", arg)
		default:
			parsed.positional = append(parsed.positional, arg)
		}
	}
	if parsed.help {
		return parsed, nil // help short-circuits flag validation
	}
	if err := validateAuthFlags(sub, parsed); err != nil {
		return authArgs{}, err
	}
	return parsed, nil
}

// validateAuthFlags rejects flags a subcommand does not accept, so an ambiguous
// invocation fails fast instead of silently ignoring a flag.
func validateAuthFlags(sub string, a authArgs) error {
	allowed := map[string]map[string]bool{
		"logout":  {"json": true},
		"status":  {"json": true},
		"refresh": {"watch": true},
		"reset":   {"json": true, "confirm": true},
	}[sub]
	bad := func(name string) error { return fmt.Errorf("qg auth %s does not accept %s", sub, name) }
	if a.json && !allowed["json"] {
		return bad("--json")
	}
	if a.watch && !allowed["watch"] {
		return bad("--watch")
	}
	if a.confirm && !allowed["confirm"] {
		return bad("--confirm")
	}
	if len(a.scopes) > 0 && !allowed["scope"] {
		return bad("--scope")
	}
	return nil
}

// newAuthManager builds an oauth.Manager backed by the file store, printing the
// authorization URL / device code to stdout. The store path honors
// QG_OAUTH_TOKENS_PATH (env), so callers/tests can redirect it. Setting
// QG_OAUTH_STORAGE=encrypted-file selects the AES-256-GCM encrypted-at-rest
// backend (a per-user secret is created beside the token file).
func newAuthManager(deps appDeps, out io.Writer) (*oauth.Manager, error) {
	// Validate QG_OAUTH_STORAGE up front: a mistyped value must fail fast rather
	// than silently change the backend. Empty = default (plaintext 0600 file);
	// "encrypted-file" = AES-256-GCM; "keyring" = the OS keyring.
	storage := strings.ToLower(strings.TrimSpace(os.Getenv("QG_OAUTH_STORAGE")))
	switch storage {
	case "", "file", "encrypted-file", "keyring":
	default:
		return nil, fmt.Errorf("invalid QG_OAUTH_STORAGE %q (supported: file, encrypted-file, keyring)", storage)
	}
	store, err := oauth.NewStore(oauth.StoreOptions{
		Now:     deps.now,
		Storage: storage,
	})
	if err != nil {
		return nil, err
	}
	return oauth.NewManager(oauth.ManagerOptions{
		Store:      store,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Now:        deps.now,
		Out:        out,
		// The opener prints the URL so headless shells can copy it; the URL
		// carries no token material. A real browser launch is intentionally not
		// performed (the sandbox/headless contexts make printing the safer default).
		OpenBrowser: func(string) error { return nil },
	})
}

func runAuthLogout(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	parsed, err := parseAuthArgs("logout", args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		_ = writeAuthHelp(stdout)
		return exitSuccess
	}
	if len(parsed.positional) != 1 {
		return writeExecUsageError(stderr, "usage: qg auth logout <provider>")
	}
	provider := parsed.positional[0]
	if descriptor, ok := providercatalog.Get(provider); ok && descriptor.ID == providercatalog.QGID {
		if parsed.json {
			return runLogout([]string{"--json"}, stdout, stderr, deps)
		}
		return runLogout(nil, stdout, stderr, deps)
	}
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	removed, err := manager.Logout(provider)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	// Also drop any stored API key and its marker so `auth logout` clears the whole
	// credential (OAuth token AND key), not just the OAuth side. Surface deletion
	// failures rather than reporting success while a credential remains.
	keyRemoved, keyErr := config.ForgetProviderKey(provider)
	if keyErr != nil {
		return writeAppError(stderr, redaction.ErrorMessage(keyErr, redaction.Options{}), exitCrash)
	}
	if configPath, perr := deps.userConfigPath(); perr == nil {
		if _, clearErr := config.ClearProviderKeyStored(configPath, provider); clearErr != nil {
			return writeAppError(stderr, redaction.ErrorMessage(clearErr, redaction.Options{}), exitCrash)
		}
	}
	removed = removed || keyRemoved
	if parsed.json {
		payload := struct {
			Provider string `json:"provider"`
			Removed  bool   `json:"removed"`
		}{Provider: provider, Removed: removed}
		if err := writePrettyJSON(stdout, payload); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	msg := fmt.Sprintf("No stored credential for %s.\n", provider)
	if removed {
		msg = fmt.Sprintf("Logged out of %s.\n", provider)
	}
	if _, err := fmt.Fprint(stdout, msg); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runAuthStatus(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	parsed, err := parseAuthArgs("status", args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		_ = writeAuthHelp(stdout)
		return exitSuccess
	}
	if len(parsed.positional) > 1 {
		return writeExecUsageError(stderr, "usage: qg auth status [provider]")
	}
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	statuses, err := manager.StatusAll()
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if len(parsed.positional) == 1 {
		statuses = filterAuthStatuses(statuses, parsed.positional[0])
	}
	if parsed.json {
		payload := struct {
			Logins []oauth.Status `json:"logins"`
		}{Logins: statuses}
		if err := writePrettyJSON(stdout, payload); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintln(stdout, oauth.FormatStatuses(statuses)); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func runAuthRefresh(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	parsed, err := parseAuthArgs("refresh", args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		_ = writeAuthHelp(stdout)
		return exitSuccess
	}
	if len(parsed.positional) != 1 {
		return writeExecUsageError(stderr, "usage: qg auth refresh <provider>")
	}
	provider := parsed.positional[0]
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	key := oauth.ProviderKey(provider)
	if parsed.watch {
		return runAuthRefreshWatch(manager, key, provider, stdout, stderr)
	}
	if _, err := manager.Handle401(context.Background(), key); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if _, err := fmt.Fprintf(stdout, "Refreshed OAuth token for %s.\n", provider); err != nil {
		return exitCrash
	}
	return exitSuccess
}

// runAuthRefreshWatch keeps a provider's token fresh in the foreground until
// interrupted. This is the opt-in proactive-refresh scheduler surface (for a
// long-running external process that reads the token file). It validates a
// refreshable token exists first, then schedules refreshes before each expiry.
func runAuthRefreshWatch(manager *oauth.Manager, key, provider string, stdout io.Writer, stderr io.Writer) int {
	ctx, stop := signalContext()
	defer stop()
	// Validate (and refresh-if-needed) once up front so a missing/non-refreshable
	// token fails fast instead of silently watching nothing.
	if _, err := manager.GetFresh(ctx, key); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	scheduler := oauth.NewRefreshScheduler()
	scheduler.Start(ctx, manager, key)
	defer scheduler.Stop()
	if _, err := fmt.Fprintf(stdout, "Watching %s — refreshing before expiry. Press Ctrl+C to stop.\n", provider); err != nil {
		return exitCrash
	}
	<-ctx.Done()
	return exitSuccess
}

func runAuthReset(args []string, stdout io.Writer, stderr io.Writer, deps appDeps) int {
	parsed, err := parseAuthArgs("reset", args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		_ = writeAuthHelp(stdout)
		return exitSuccess
	}
	if len(parsed.positional) > 0 {
		return writeExecUsageError(stderr, fmt.Sprintf("qg auth reset takes no arguments (got %q)", parsed.positional[0]))
	}
	if !parsed.confirm {
		return writeExecUsageError(stderr, "qg auth reset clears all stored OAuth logins and shared MCP tokens; rerun with --confirm to reset")
	}
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if err := manager.Reset(); err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if parsed.json {
		payload := struct {
			Reset bool `json:"reset"`
		}{Reset: true}
		if err := writePrettyJSON(stdout, payload); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	if _, err := fmt.Fprintln(stdout, "Reset OAuth token store."); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func filterAuthStatuses(statuses []oauth.Status, provider string) []oauth.Status {
	want := oauth.ProviderKey(provider)
	filtered := make([]oauth.Status, 0, 1)
	for _, st := range statuses {
		if st.Key == want {
			filtered = append(filtered, st)
		}
	}
	return filtered
}

func writeAuthHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Usage:
  qg auth <command>

Commands:
  logout <provider>                               Delete a provider's stored login
  status [provider]                               Show login presence/expiry (never the token)
  refresh <provider> [--watch]                    Force a token refresh (--watch keeps it fresh)
  reset --confirm [--json]                        Clear all OAuth logins and shared MCP tokens (also repairs corrupted stores)

OAuth login was removed: run "qg login" to save your API key — QG_API_KEY
(or QUEBRAGALHO_API_KEY) from the environment also works; see "qg setup
--help". These commands maintain token stores written by earlier versions.

Note: "qg auth status" reports OAuth token stores only. It does not cover
the Quebra Galho provider API key (credential store / QG_API_KEY); use
"qg key" for that.

Storage: tokens are written 0600 under $XDG_CONFIG_HOME/qg (override with
QG_OAUTH_TOKENS_PATH). Set QG_OAUTH_STORAGE=encrypted-file to encrypt them at
rest with AES-256-GCM (a per-user secret beside the file), or
QG_OAUTH_STORAGE=keyring to use the OS keyring (macOS Keychain / Linux
secret-tool). MCP server tokens share the same store.

Flags:
      --watch    Keep the token fresh in the foreground (refresh only)
      --json     Print result as JSON (status/logout/reset)
  -h, --help     Show this help
`)
	return err
}
