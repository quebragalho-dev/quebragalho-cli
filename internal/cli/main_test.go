package cli

import (
	"os"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// TestMain pins the credential backend to a plaintext file for every test in
// this package. Without this, any test that resolves the credential store
// without pinning QG_CRED_STORAGE itself falls back to keyring-first on
// darwin, which execs the real `security` binary and pops a Keychain dialog
// on the developer's screen. Per-test t.Setenv calls still override this for
// the tests that pin encrypted-file or bogus storage themselves.
//
// On Windows the sandbox runs a confined command by re-executing the current
// binary with a hidden subcommand (see runWithDeps). Under `go test` that
// binary is this test binary, so TestMain answers the same subcommands;
// without it every sandboxed spawn, an MCP stdio server included, fails.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case sandbox.WindowsCommandRunnerSubcommand:
			os.Exit(sandbox.RunWindowsSandboxCommandRunner(os.Args[2:], os.Stderr))
		case sandbox.WindowsSandboxSetupSubcommand:
			os.Exit(sandbox.RunWindowsSandboxSetup(os.Args[2:], os.Stderr))
		}
	}
	if err := os.Setenv("QG_CRED_STORAGE", "file"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
