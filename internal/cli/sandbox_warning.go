package cli

import (
	"fmt"
	"io"

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
