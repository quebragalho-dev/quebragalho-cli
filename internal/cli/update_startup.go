package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/browser"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
	"github.com/quebragalho-dev/quebragalho-cli/internal/updatenotice"
)

// errUpdateStartupNoWrapper is reported when an npm install is requested
// without the wrapper handoff file, so there is nowhere to deliver it.
var errUpdateStartupNoWrapper = errors.New("instalação npm indisponível fora do wrapper")

// updateStartupDeps wires the interactive startup update check. Tests inject
// fakes; the app passes the real providers.
type updateStartupDeps struct {
	currentVersion string
	// installMethod overrides detection ("" detects from the executable).
	installMethod update.InstallMethod
	// stdinIsTTY gates the prompt: it only ever shows on a real terminal.
	stdinIsTTY bool
	env        func(string) string
	now        func() time.Time
	// stateFile overrides the stored-state location ("" resolves it).
	stateFile string
	args      []string
	// runPrompt shows the startup screen; nil proceeds without asking.
	runPrompt func(ctx context.Context, current, latest, changelogURL string, openURL func(string) error, install func(ctx context.Context) error) (tui.UpdateChoice, error)
	openURL   func(url string) error
	// applyUpdate installs the new standalone binary, returning its path.
	applyUpdate func(ctx context.Context) (string, error)
	// relaunch replaces the process with the new binary (Unix) or runs it and
	// reports its exit code (Windows).
	relaunch func(newBinary string, args []string, env []string) (int, error)
}

func (deps updateStartupDeps) lookupEnv(key string) string {
	if deps.env != nil {
		return deps.env(key)
	}
	return os.Getenv(key)
}

func (deps updateStartupDeps) clock() func() time.Time {
	if deps.now != nil {
		return deps.now
	}
	return time.Now
}

// runUpdateStartup implements the interactive startup update step: it never
// waits on the network, offers the new version through the TUI prompt when
// one is known, installs on choice, and returns whether the TUI should open.
func runUpdateStartup(ctx context.Context, stderr io.Writer, deps updateStartupDeps) (exitCode int, proceed bool) {
	env := deps.lookupEnv
	// QG_UPDATE_JUST_INSTALLED is a one-shot handoff: consume it here so it
	// never leaks into child processes of this session.
	justInstalled := strings.TrimSpace(env("QG_UPDATE_JUST_INSTALLED"))
	_ = os.Unsetenv("QG_UPDATE_JUST_INSTALLED")
	if !deps.stdinIsTTY {
		return 0, true
	}
	current := strings.TrimSpace(deps.currentVersion)
	if justInstalled != "" && justInstalled != current {
		fmt.Fprintf(stderr, "[qg] a atualização para %s não ficou ativa. Abrindo a versão atual.\n", justInstalled)
		return 0, true
	}
	method := deps.installMethod
	if method == "" {
		executable, err := os.Executable()
		if err != nil {
			method = update.InstallMethodStandalone
		} else {
			method = update.DetectInstallMethod(executable)
		}
	}
	config := updatenotice.Config{
		CurrentVersion: current,
		InstallMethod:  method,
		Env:            deps.env,
		Now:            deps.clock(),
		NpmEndpoint:    strings.TrimSpace(env("QG_UPDATE_NPM_ENDPOINT")),
	}
	if !updatenotice.Enabled(config) {
		return 0, true
	}
	stateFile := deps.stateFile
	if stateFile == "" {
		resolved, err := updatenotice.StatePath(deps.env)
		if err != nil {
			return 0, true
		}
		stateFile = resolved
	}
	state := updatenotice.Load(stateFile)
	if updatenotice.Due(config, state) {
		refreshCtx, cancel := context.WithTimeout(ctx, update.DefaultTimeout)
		go func() {
			defer cancel()
			// Check first, merge after: Update re-reads the file, so a
			// refresh that finishes after the user pressed "skip" keeps the
			// recorded skip instead of blanking it with a stale copy.
			refreshed := updatenotice.Refresh(refreshCtx, config, updatenotice.State{})
			_ = updatenotice.Update(stateFile, func(latest *updatenotice.State) {
				latest.LastCheck = refreshed.LastCheck
				if refreshed.LatestVersion != "" {
					latest.LatestVersion = refreshed.LatestVersion
				}
			})
		}()
	}
	version := updatenotice.PromptVersion(config, state)
	if version == "" || deps.runPrompt == nil {
		return 0, true
	}
	openURL := deps.openURL
	if openURL == nil {
		openURL = func(url string) error { return browser.Open(ctx, url) }
	}
	handoffPath := strings.TrimSpace(env("QG_NPM_UPDATE_HANDOFF"))
	var installedBinary string
	install := func(ctx context.Context) error {
		if handoffPath != "" {
			payload, err := json.Marshal(map[string]string{"version": version})
			if err != nil {
				return err
			}
			return os.WriteFile(handoffPath, append(payload, '\n'), 0o600)
		}
		if method == update.InstallMethodNpm {
			return errUpdateStartupNoWrapper
		}
		path, err := deps.applyUpdate(ctx)
		if err != nil {
			return err
		}
		installedBinary = strings.TrimSpace(path)
		return nil
	}
	choice, err := deps.runPrompt(ctx, current, version, updatenotice.ChangelogURL(version), openURL, install)
	if err != nil {
		fmt.Fprintf(stderr, "[qg] Não foi possível exibir o aviso de atualização. Iniciando a versão atual.\n")
		return 0, true
	}
	switch choice {
	case tui.UpdateQuit:
		return 0, false
	case tui.UpdateInstall:
		if handoffPath != "" {
			// The wrapper reinstalls and reopens; this process just exits.
			return 0, false
		}
		if installedBinary == "" {
			return 0, true
		}
		relaunchArgs := []string{installedBinary}
		relaunchArgs = append(relaunchArgs, updateStartupArgs(deps)...)
		envList := append(append([]string{}, os.Environ()...), "QG_UPDATE_JUST_INSTALLED="+version)
		code, err := deps.relaunch(installedBinary, relaunchArgs, envList)
		if err != nil {
			return writeUpdateInstallFailure(stderr, err), true
		}
		return code, false
	case tui.UpdateInstallFailed:
		return 0, true
	default: // UpdateSkip: the only choice that records a refusal.
		_ = updatenotice.Update(stateFile, func(s *updatenotice.State) {
			s.SkippedVersion = version
		})
		return 0, true
	}
}

// updateStartupArgs returns the original command line minus argv[0] so the
// relaunched binary inherits the same invocation.
func updateStartupArgs(deps updateStartupDeps) []string {
	args := deps.args
	if len(args) == 0 {
		args = os.Args
	}
	if len(args) <= 1 {
		return nil
	}
	return append([]string{}, args[1:]...)
}

// writeUpdateInstallFailure prints the one-line relaunch failure and keeps
// the current version open. Install failures surface inside the prompt
// itself; this path only covers a relaunch that breaks after a good install.

func writeUpdateInstallFailure(stderr io.Writer, err error) int {
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if runes := []rune(reason); len(runes) > 200 {
		reason = string(runes[:200]) + "…"
	}
	fmt.Fprintf(stderr, "[qg] Não foi possível atualizar: %s. Iniciando a versão atual.\n", reason)
	return 0
}
