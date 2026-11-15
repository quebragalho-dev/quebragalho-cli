package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/daemon"
	"github.com/quebragalho-dev/quebragalho-cli/internal/daemon/remote"
)

// O qg daemon responde em português na ajuda, no status e nos erros de uso.
func TestPTBRDaemonHelpStatusAndUsageErrors(t *testing.T) {
	isolateDaemonPaths(t)

	if code, out, _ := runDaemonCLI(t, "--help"); code != exitSuccess || !strings.Contains(out, "Uso: qg daemon") {
		t.Fatalf("--help exit=%d out=%q", code, out)
	}
	for _, tc := range []struct {
		args []string
		want string
		bad  []string
	}{
		{[]string{"stop", "oops"}, "daemon stop não aceita argumentos", []string{"takes no arguments"}},
		{[]string{"status", "oops"}, "daemon status não aceita argumentos", []string{"takes no arguments"}},
		{[]string{"run", "--prompt", "hi"}, "daemon run exige --session <id>", []string{"requires --session"}},
		{[]string{"run", "--session", "s1"}, "daemon run exige --prompt <texto> ou argumentos do exec", []string{"requires --prompt", "or exec args"}},
		{[]string{"attach"}, "daemon attach exige um id <session>", []string{"requires a <session> id"}},
		{[]string{"attach", "s1", "extra"}, "daemon attach aceita exatamente um id <session>", []string{"accepts exactly one"}},
		{[]string{"serve-remote", "--addr", "x", "--bogus"}, "flag desconhecida", []string{"unknown flag"}},
		{[]string{"serve-remote"}, "daemon serve-remote exige --addr <host:port>", []string{"requires --addr"}},
		{[]string{"link"}, "daemon link exige --remote, --repo e --id (ou --show <file>)", []string{"requires --remote", "and --id"}},
	} {
		_, _, errb := runDaemonCLI(t, tc.args...)
		if !strings.Contains(errb, tc.want) {
			t.Fatalf("%v stderr missing %q, got %q", tc.args, tc.want, errb)
		}
		for _, unwanted := range tc.bad {
			if strings.Contains(errb, unwanted) {
				t.Fatalf("%v stderr leaked English %q, got %q", tc.args, unwanted, errb)
			}
		}
	}

	if _, out, _ := runDaemonCLI(t, "status"); !strings.Contains(out, "não está em execução") || strings.Contains(out, "not running") {
		t.Fatalf("status output wrong: %q", out)
	}
	if _, _, errb := runDaemonCLI(t, "run", "--session", "s1", "--prompt", "oi"); !strings.Contains(errb, "não está em execução") || strings.Contains(errb, "not running") {
		t.Fatalf("run dial error wrong: %q", errb)
	}

	// Sentinelas do daemon falam português sem perder a identidade por errors.Is.
	for _, tc := range []struct {
		err  error
		want string
		bad  string
	}{
		{daemon.ErrAlreadyRunning, "já há outra instância em execução", "another instance is already running"},
		{daemon.ErrPoolDraining, "o pool está encerrando", "pool is draining"},
		{daemon.ErrSessionExists, "a sessão já existe", "session already exists"},
		{daemon.ErrSessionNotFound, "sessão não encontrada", "session not found"},
	} {
		if !strings.Contains(tc.err.Error(), tc.want) || strings.Contains(tc.err.Error(), tc.bad) {
			t.Fatalf("sentinel = %q", tc.err.Error())
		}
	}
	if !errors.Is(daemon.ErrSessionNotFound, daemon.ErrSessionNotFound) {
		t.Fatal("sentinel identity broken")
	}
	for _, tc := range []struct {
		err  error
		want string
		bad  string
	}{
		{remote.ErrUnauthorized, "não autorizado", "unauthorized"},
	} {
		if !strings.Contains(tc.err.Error(), tc.want) || strings.Contains(tc.err.Error(), tc.bad) {
			t.Fatalf("remote sentinel = %q", tc.err.Error())
		}
	}
}

func TestPTBRDaemonTokenFromEnvHint(t *testing.T) {
	t.Setenv(remote.EnvToken, "")
	t.Setenv(remote.EnvTokenFile, "")
	if _, err := remote.TokenFromEnv(); err == nil || !strings.Contains(err.Error(), "defina "+remote.EnvToken) {
		t.Fatalf("TokenFromEnv = %v, want Portuguese hint", err)
	}
}
