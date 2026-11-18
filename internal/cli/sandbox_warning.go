package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// sandboxUnavailableSessionWarningText is the one-time session warning shown
// when the sandbox is enabled but the native backend is unavailable: commands
// still run, without native isolation. Short Portuguese, deliberately neutral
// about the fix: the same warning fires on Linux (broken or missing bwrap),
// on Windows before `qg sandbox setup`, and on WSL, and only `qg doctor`
// knows each platform's remediation.
const sandboxUnavailableSessionWarningText = "Ambiente isolado indisponível: os comandos rodam sem isolamento nativo. Rode `qg doctor` para ver como resolver."

// sandboxUnavailableSessionWarning returns the session warning when the
// sandbox is enabled but no native backend is available (broken or missing
// bwrap, missing helper, WSL, ...). It returns "" when the user disabled the
// sandbox on purpose (sandbox.enabled == false), when the backend works, and
// inside an already isolated process (QG_SANDBOXED=1 with a backend marker),
// where re-warning would be noise: nested commands are already wrapped.
func sandboxUnavailableSessionWarning(sandboxEnabled bool, backend sandbox.Backend) string {
	if !sandboxEnabled {
		return ""
	}
	if backend.Available {
		return ""
	}
	if sandbox.IsAlreadySandboxed() {
		return ""
	}
	return sandboxUnavailableSessionWarningText
}

// emitSandboxUnavailableWarning writes the session warning once to stderr
// (never stdout: exec stream-json/text output must stay clean).
func emitSandboxUnavailableWarning(stderr io.Writer, sandboxEnabled bool, backend sandbox.Backend) {
	if warning := sandboxUnavailableSessionWarning(sandboxEnabled, backend); warning != "" {
		_, _ = fmt.Fprintln(stderr, warning)
	}
}

// sandboxSetupPendingWarningText is the one-time session warning when the
// Windows native backend is installed but `qg sandbox setup` never ran:
// commands still run (the unelevated tier keeps the filesystem write-jail),
// but without OS-level network enforcement. Short Portuguese, naming the fix.
const sandboxSetupPendingWarningText = "Sandbox do Windows sem setup: os comandos rodam com isolamento parcial. Rode `qg sandbox setup` em um terminal elevado para isolamento total."

// sandboxSetupPendingWarning returns the setup warning when the Windows
// sandbox helper is available yet the setup marker is missing. It returns ""
// when the sandbox is disabled, off Windows, without the restricted-token
// backend, with an unavailable backend (covered by the unavailable warning),
// inside an already isolated process, or once setup has run.
func sandboxSetupPendingWarning(sandboxEnabled bool, backend sandbox.Backend, goos string) string {
	if !sandboxEnabled {
		return ""
	}
	if goos != "windows" || backend.Name != sandbox.BackendWindowsRestrictedToken {
		return ""
	}
	if !backend.Available {
		return ""
	}
	if sandbox.IsAlreadySandboxed() {
		return ""
	}
	if sandbox.WindowsSandboxInitialized() {
		return ""
	}
	return sandboxSetupPendingWarningText
}

// firstSandboxSessionWarning returns the warning the session should show once:
// the unavailable-backend warning when native isolation is missing, else the
// Windows setup-pending warning when the helper waits for `qg sandbox setup`.
func firstSandboxSessionWarning(sandboxEnabled bool, backend sandbox.Backend) string {
	if warning := sandboxUnavailableSessionWarning(sandboxEnabled, backend); warning != "" {
		return warning
	}
	return sandboxSetupPendingWarning(sandboxEnabled, backend, runtime.GOOS)
}

// emitSandboxSetupPendingWarning writes the setup warning once to stderr
// (never stdout: exec stream-json/text output must stay clean).
func emitSandboxSetupPendingWarning(stderr io.Writer, sandboxEnabled bool, backend sandbox.Backend, goos string) {
	if warning := sandboxSetupPendingWarning(sandboxEnabled, backend, goos); warning != "" {
		_, _ = fmt.Fprintln(stderr, warning)
	}
}
