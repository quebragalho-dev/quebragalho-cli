package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/background"
	"github.com/quebragalho-dev/quebragalho-cli/internal/daemon"
	"github.com/quebragalho-dev/quebragalho-cli/internal/daemon/remote"
)

// runDaemon dispatches the `qg daemon ...` subcommands. The daemon supervises a
// pool of headless `qg exec` workers and routes sessions to them over an
// owner-only local control socket. It is an ADDITIVE surface — interactive and
// one-shot exec are unchanged.
func runDaemon(args []string, stdout io.Writer, stderr io.Writer, _ appDeps) int {
	if len(args) == 0 {
		return writeDaemonUsage(stderr, exitUsage)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "start":
		return runDaemonStart(rest, stdout, stderr)
	case "stop":
		return runDaemonStop(rest, stdout, stderr)
	case "status":
		return runDaemonStatus(rest, stdout, stderr)
	case "run":
		return runDaemonRun(rest, stdout, stderr)
	case "attach":
		return runDaemonAttach(rest, stdout, stderr)
	case "serve-remote":
		return runDaemonServeRemote(rest, stdout, stderr)
	case "link":
		return runDaemonLink(rest, stdout, stderr)
	case "-h", "--help", "help":
		return writeDaemonUsage(stdout, exitSuccess)
	default:
		fmt.Fprintf(stderr, "subcomando desconhecido do daemon %q\n", sub)
		return writeDaemonUsage(stderr, exitUsage)
	}
}

func writeDaemonUsage(w io.Writer, code int) int {
	fmt.Fprint(w, `Uso: qg daemon <command>

Comandos:
  start [--foreground]      Inicia o daemon (em segundo plano por padrão).
  stop                      Para o daemon em execução com cuidado.
  status                    Mostra o estado do daemon / pool / sessão.
  run --session <id> [--cwd <dir>] [--prompt <text>] [exec flags...]
                            Cria/encaminha uma sessão e transmite a saída.
  attach <session>          Anexa ao stream de uma sessão em execução.
  serve-remote --addr <host:port> --tls-cert <f> --tls-key <f>
      [--bundle-dir <d>]
                            Serve uma ponte de rede opt-in, só TLS, para
                            este daemon. Exige bearer token em
                            $QG_DAEMON_REMOTE_TOKEN (ou
                            $QG_DAEMON_REMOTE_TOKEN_FILE). --bundle-dir
                            ativa uploads de git-bundle, extraídos
                            em worktrees por link.
  link --remote <host:port> --repo <dir> --id <name> [--out <file>]
                            Envia o histórico git do repo para o remoto
                            como bundle e imprime o caminho remoto
                            extraído. --out salva um arquivo de link
                            de sessão (0600). Aceita
                            --token/--ca-cert/--server-name.
  link --show <file>        Imprime um link de sessão salvo.

run e attach aceitam --remote <host:port> [--token <t>] [--ca-cert <f>]
[--server-name <name>] para usar um daemon remoto pela ponte em vez do
socket local. Use o caminho remoto do link como --cwd para rodar
contra um repo linkado.
`)
	return code
}

// runDaemonStart starts the daemon. Without --foreground it spawns a detached
// background process running the foreground daemon and returns once it is up.
func runDaemonStart(args []string, stdout io.Writer, stderr io.Writer) int {
	foreground := false
	for _, a := range args {
		switch a {
		case "--foreground", "-f":
			foreground = true
		case "-h", "--help":
			return writeDaemonUsage(stdout, exitSuccess)
		default:
			return writeExecUsageError(stderr, fmt.Sprintf("flag desconhecida %q para daemon start", a))
		}
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	if foreground {
		return runDaemonForeground(paths, stdout, stderr)
	}
	return runDaemonStartDetached(paths, stdout, stderr)
}

// runDaemonForeground runs the daemon in this process until SIGINT/SIGTERM. Each
// worker is a headless `qg exec` child with the sandbox re-entrancy markers
// scrubbed (NewExecLauncher) so it establishes its own sandbox.
func runDaemonForeground(paths daemon.Paths, stdout io.Writer, stderr io.Writer) int {
	launcher, err := daemon.NewExecLauncher(daemon.ExecLauncherConfig{})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	logf := func(line string) { fmt.Fprintln(stderr, "[daemon] "+line) }
	pool, err := daemon.NewPool(daemon.PoolOptions{Launcher: launcher, Log: logf})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	mgr, err := daemon.NewSessionManager(daemon.SessionManagerOptions{Pool: pool})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	srv, err := daemon.NewServer(daemon.ServerOptions{Paths: paths, Manager: mgr, Pool: pool, Log: logf})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.Shutdown()
	}()

	if err := srv.Serve(); err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	return exitSuccess
}

// runDaemonStartDetached spawns the foreground daemon as a detached background
// process (its own process group, output to a log file) and waits for it to bind.
func runDaemonStartDetached(paths daemon.Paths, stdout io.Writer, stderr io.Writer) int {
	if daemonReachable(paths) {
		fmt.Fprintln(stdout, "o qg daemon já está em execução")
		return exitSuccess
	}
	exe, err := os.Executable()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	logFile, logPath, err := daemon.OpenRuntimeLog(paths)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon", "start", "--foreground")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	background.ConfigureChildProcessGroup(cmd) // own process group: outlives this shell
	if err := startAndAwaitDaemonProcess(cmd, func() bool { return daemonReachable(paths) }, 5*time.Second, 25*time.Millisecond); err != nil {
		if errors.Is(err, errDaemonStartTimeout) {
			return writeAppError(stderr, err.Error()+"; veja "+logPath, exitCrash)
		}
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	fmt.Fprintf(stdout, "qg daemon iniciado (socket %s)\n", paths.Socket)
	return exitSuccess
}

var errDaemonStartTimeout = errors.New("o daemon não subiu dentro do tempo limite")

func startAndAwaitDaemonProcess(cmd *exec.Cmd, reachable func() bool, timeout time.Duration, pollInterval time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if waitForDaemonReadiness(reachable, timeout, pollInterval) {
		if err := cmd.Process.Release(); err != nil {
			cleanupErr := terminateAndReapDaemonProcess(cmd)
			if cleanupErr != nil {
				return fmt.Errorf("liberar o processo do daemon: %w (falha na limpeza: %v)", err, cleanupErr)
			}
			return fmt.Errorf("liberar o processo do daemon: %w", err)
		}
		return nil
	}
	if err := terminateAndReapDaemonProcess(cmd); err != nil {
		return fmt.Errorf("%w; falha na limpeza: %v", errDaemonStartTimeout, err)
	}
	return errDaemonStartTimeout
}

func waitForDaemonReadiness(reachable func() bool, timeout time.Duration, pollInterval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if reachable() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		time.Sleep(min(pollInterval, remaining))
	}
	// The loop's last reachable() call already ran immediately before this one
	// (right after the final Sleep, with the same deadline check separating
	// them), so this adds no extra waiting time and is not a grace window for a
	// slow-starting daemon — it only re-checks the same instant in case
	// reachable() itself is non-deterministic. To tolerate a genuinely slow
	// start, raise the timeout passed to startAndAwaitDaemonProcess instead.
	return reachable()
}

func terminateAndReapDaemonProcess(cmd *exec.Cmd) error {
	return background.TerminateCommand(cmd)
}

func runDaemonStop(args []string, stdout io.Writer, stderr io.Writer) int {
	if helpRequested(args) {
		return writeDaemonUsage(stdout, exitSuccess)
	}
	if len(args) > 0 {
		return writeExecUsageError(stderr, "daemon stop não aceita argumentos")
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	client, err := daemon.Dial(paths.Socket)
	if err != nil {
		fmt.Fprintln(stdout, "o qg daemon não está em execução")
		return exitSuccess
	}
	defer client.Close()
	if err := client.Shutdown(); err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	fmt.Fprintln(stdout, "qg daemon parado")
	return exitSuccess
}

func runDaemonStatus(args []string, stdout io.Writer, stderr io.Writer) int {
	if helpRequested(args) {
		return writeDaemonUsage(stdout, exitSuccess)
	}
	if len(args) > 0 {
		return writeExecUsageError(stderr, "daemon status não aceita argumentos")
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	client, err := daemon.Dial(paths.Socket)
	if err != nil {
		fmt.Fprintln(stdout, "o qg daemon não está em execução")
		return exitSuccess
	}
	defer client.Close()
	report, err := client.Status()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	fmt.Fprintf(stdout, "daemon pid=%d versão=%d socket=%s\n", report.PID, report.Version, report.Socket)
	fmt.Fprintf(stdout, "pool tamanho=%d ocupados=%d fila=%d\n", report.PoolSize, len(report.Workers), report.QueueDepth)
	for _, s := range report.Sessions {
		fmt.Fprintf(stdout, "  sessão %s estado=%s linhas=%d\n", s.ID, s.State, s.Lines)
	}
	return exitSuccess
}

func runDaemonRun(args []string, stdout io.Writer, stderr io.Writer) int {
	session, cwd, prompt := "", "", ""
	var remoteFlags remoteDialFlags
	var forward []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "-h" || a == "--help":
			return writeDaemonUsage(stdout, exitSuccess)
		case a == "--remote":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--remote exige um valor")
			}
			remoteFlags.Addr = v
		case strings.HasPrefix(a, "--remote="):
			remoteFlags.Addr = strings.TrimPrefix(a, "--remote=")
		case a == "--token":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--token exige um valor")
			}
			remoteFlags.Token = v
		case strings.HasPrefix(a, "--token="):
			remoteFlags.Token = strings.TrimPrefix(a, "--token=")
		case a == "--ca-cert":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--ca-cert exige um valor")
			}
			remoteFlags.CACert = v
		case strings.HasPrefix(a, "--ca-cert="):
			remoteFlags.CACert = strings.TrimPrefix(a, "--ca-cert=")
		case a == "--server-name":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--server-name exige um valor")
			}
			remoteFlags.ServerName = v
		case strings.HasPrefix(a, "--server-name="):
			remoteFlags.ServerName = strings.TrimPrefix(a, "--server-name=")
		case a == "--session":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--session exige um valor")
			}
			session = v
		case strings.HasPrefix(a, "--session="):
			session = strings.TrimPrefix(a, "--session=")
		case a == "--cwd":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--cwd exige um valor")
			}
			cwd = v
		case strings.HasPrefix(a, "--cwd="):
			cwd = strings.TrimPrefix(a, "--cwd=")
		case a == "--prompt" || a == "-p":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--prompt exige um valor")
			}
			prompt = v
		case strings.HasPrefix(a, "--prompt="):
			prompt = strings.TrimPrefix(a, "--prompt=")
		default:
			// Forwarded verbatim to the worker `qg exec` (reuses exec's own flag
			// parsing for per-session run options).
			forward = append(forward, a)
		}
	}
	if strings.TrimSpace(session) == "" {
		return writeExecUsageError(stderr, "daemon run exige --session <id>")
	}
	if prompt == "" && len(forward) == 0 {
		return writeExecUsageError(stderr, "daemon run exige --prompt <texto> ou argumentos do exec")
	}
	client, err := dialForCLI(remoteFlags)
	if err != nil {
		return writeAppError(stderr, daemonDialError(remoteFlags, err), exitCrash)
	}
	defer client.Close()
	if err := client.Run(session, cwd, prompt, forward, func(line string) { fmt.Fprintln(stdout, line) }); err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	return exitSuccess
}

func runDaemonAttach(args []string, stdout io.Writer, stderr io.Writer) int {
	session := ""
	var remoteFlags remoteDialFlags
	extra := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "-h" || a == "--help":
			return writeDaemonUsage(stdout, exitSuccess)
		case a == "--remote":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--remote exige um valor")
			}
			remoteFlags.Addr = v
		case strings.HasPrefix(a, "--remote="):
			remoteFlags.Addr = strings.TrimPrefix(a, "--remote=")
		case a == "--token":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--token exige um valor")
			}
			remoteFlags.Token = v
		case strings.HasPrefix(a, "--token="):
			remoteFlags.Token = strings.TrimPrefix(a, "--token=")
		case a == "--ca-cert":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--ca-cert exige um valor")
			}
			remoteFlags.CACert = v
		case strings.HasPrefix(a, "--ca-cert="):
			remoteFlags.CACert = strings.TrimPrefix(a, "--ca-cert=")
		case a == "--server-name":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--server-name exige um valor")
			}
			remoteFlags.ServerName = v
		case strings.HasPrefix(a, "--server-name="):
			remoteFlags.ServerName = strings.TrimPrefix(a, "--server-name=")
		case session == "":
			session = a
		default:
			extra++
		}
	}
	if strings.TrimSpace(session) == "" {
		return writeExecUsageError(stderr, "daemon attach exige um id <session>")
	}
	if extra > 0 {
		return writeExecUsageError(stderr, "daemon attach aceita exatamente um id <session>")
	}
	client, err := dialForCLI(remoteFlags)
	if err != nil {
		return writeAppError(stderr, daemonDialError(remoteFlags, err), exitCrash)
	}
	defer client.Close()
	if err := client.Attach(session, func(line string) { fmt.Fprintln(stdout, line) }); err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	return exitSuccess
}

// daemonDialError tailors the connect-failure message for local vs remote.
func daemonDialError(flags remoteDialFlags, err error) string {
	if strings.TrimSpace(flags.Addr) != "" {
		return "falha ao alcançar o daemon remoto em " + flags.Addr + ": " + err.Error()
	}
	return "o qg daemon não está em execução (inicie com `qg daemon start`)"
}

// runDaemonServeRemote starts the local daemon plus an opt-in, TLS-only network
// bridge. TLS and a bearer token are mandatory (fail closed): it refuses to
// start without a cert/key pair and a token from the environment.
func runDaemonServeRemote(args []string, stdout io.Writer, stderr io.Writer) int {
	addr, certFile, keyFile, bundleDir := "", "", "", ""
	minVersion, maxConns := 0, 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "-h" || a == "--help":
			return writeDaemonUsage(stdout, exitSuccess)
		case a == "--addr":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--addr exige um valor")
			}
			addr = v
		case strings.HasPrefix(a, "--addr="):
			addr = strings.TrimPrefix(a, "--addr=")
		case a == "--tls-cert":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--tls-cert exige um valor")
			}
			certFile = v
		case strings.HasPrefix(a, "--tls-cert="):
			certFile = strings.TrimPrefix(a, "--tls-cert=")
		case a == "--tls-key":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--tls-key exige um valor")
			}
			keyFile = v
		case strings.HasPrefix(a, "--tls-key="):
			keyFile = strings.TrimPrefix(a, "--tls-key=")
		case a == "--min-version":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--min-version exige um valor")
			}
			minVersion = atoiOrZero(v)
		case a == "--max-conns":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--max-conns exige um valor")
			}
			maxConns = atoiOrZero(v)
		case a == "--bundle-dir":
			v, ok := value()
			if !ok {
				return writeExecUsageError(stderr, "--bundle-dir exige um valor")
			}
			bundleDir = v
		case strings.HasPrefix(a, "--bundle-dir="):
			bundleDir = strings.TrimPrefix(a, "--bundle-dir=")
		default:
			return writeExecUsageError(stderr, fmt.Sprintf("flag desconhecida %q para daemon serve-remote", a))
		}
	}
	if strings.TrimSpace(addr) == "" {
		return writeExecUsageError(stderr, "daemon serve-remote exige --addr <host:port>")
	}
	// Fail closed: TLS cert/key + a bearer token are mandatory.
	tlsConfig, err := remote.ServerTLSConfig(certFile, keyFile)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	token, err := remote.TokenFromEnv()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	auth, err := remote.NewTokenAuthenticator(token)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	launcher, err := daemon.NewExecLauncher(daemon.ExecLauncherConfig{})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	logf := func(line string) { fmt.Fprintln(stderr, "[daemon] "+line) }
	pool, err := daemon.NewPool(daemon.PoolOptions{Launcher: launcher, Log: logf})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	mgr, err := daemon.NewSessionManager(daemon.SessionManagerOptions{Pool: pool})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	srv, err := daemon.NewServer(daemon.ServerOptions{Paths: paths, Manager: mgr, Pool: pool, Log: logf})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	bridge, err := remote.NewBridge(remote.BridgeOptions{
		Server: srv, Authenticator: auth, MinVersion: minVersion, MaxConnections: maxConns, BundleDir: bundleDir, Log: logf,
	})
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}

	// Serve the local control socket too, so local clients keep working.
	go func() {
		if serveErr := srv.Serve(); serveErr != nil {
			logf("local serve error: " + serveErr.Error())
		}
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- bridge.ListenAndServeTLS(addr, tlsConfig) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	fmt.Fprintf(stdout, "ponte remota do qg daemon escutando em %s (TLS)\n", addr)
	select {
	case <-sigCh:
		srv.Shutdown()
		_ = bridge.Close()
		<-serveErr // wait for the accept loop to unwind
		return exitSuccess
	case err := <-serveErr:
		// Bind/serve failed before any signal (e.g. address in use).
		srv.Shutdown()
		return writeAppError(stderr, err.Error(), exitCrash)
	}
}

// runDaemonLink uploads a repo's git history to a remote bridge as a bundle
// (link without --show), or prints a saved session link (--show <file>).
func runDaemonLink(args []string, stdout io.Writer, stderr io.Writer) int {
	var addr, repo, id, token, caCert, serverName, out, show string
	// flags maps each "--name" to the string it sets; both "--name v" and
	// "--name=v" forms are accepted.
	flags := map[string]*string{
		"--remote": &addr, "--repo": &repo, "--id": &id, "--token": &token,
		"--ca-cert": &caCert, "--server-name": &serverName, "--out": &out, "--show": &show,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" || a == "--help" {
			return writeDaemonUsage(stdout, exitSuccess)
		}
		name, inlineVal, hasInline := a, "", false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name, inlineVal, hasInline = a[:eq], a[eq+1:], true
		}
		dst, ok := flags[name]
		if !ok {
			return writeExecUsageError(stderr, fmt.Sprintf("flag desconhecida %q para daemon link", a))
		}
		if hasInline {
			*dst = inlineVal
			continue
		}
		if i+1 >= len(args) {
			return writeExecUsageError(stderr, fmt.Sprintf("%s exige um valor", name))
		}
		i++
		*dst = args[i]
	}

	if strings.TrimSpace(show) != "" {
		link, err := remote.LoadSessionLink(show)
		if err != nil {
			return writeAppError(stderr, err.Error(), exitCrash)
		}
		fmt.Fprintf(stdout, "link %s -> %s em %s\n", link.LinkID, link.RemotePath, link.Address)
		return exitSuccess
	}

	if strings.TrimSpace(addr) == "" || strings.TrimSpace(repo) == "" || strings.TrimSpace(id) == "" {
		return writeExecUsageError(stderr, "daemon link exige --remote, --repo e --id (ou --show <file>)")
	}
	if strings.TrimSpace(token) == "" {
		token, _ = remote.TokenFromEnv() // best effort; UploadRepoBundle rejects an empty token
	}
	link, err := remote.UploadRepoBundle(remote.RemoteConfig{
		Address:    addr,
		Token:      token,
		CACertFile: caCert,
		ServerName: serverName,
	}, repo, id)
	if err != nil {
		return writeAppError(stderr, err.Error(), exitCrash)
	}
	fmt.Fprintf(stdout, "%s enviado; repo remoto em %s\n", link.LinkID, link.RemotePath)
	fmt.Fprintf(stdout, "rode com: qg daemon run --remote %s --cwd %s ...\n", link.Address, link.RemotePath)
	if strings.TrimSpace(out) != "" {
		if err := link.Save(out); err != nil {
			return writeAppError(stderr, err.Error(), exitCrash)
		}
		fmt.Fprintf(stdout, "link de sessão salvo em %s\n", out)
	}
	return exitSuccess
}

// remoteDialFlags holds the optional flags that redirect run/attach to a remote
// daemon. When Addr is empty the local control socket is used.
type remoteDialFlags struct {
	Addr       string
	Token      string
	CACert     string
	ServerName string
}

// dialForCLI returns a daemon.Client for either the local socket (Addr empty) or
// a remote bridge (Addr set). For remote, the token falls back to the env.
func dialForCLI(flags remoteDialFlags) (*daemon.Client, error) {
	if strings.TrimSpace(flags.Addr) == "" {
		paths, err := daemon.DefaultPaths()
		if err != nil {
			return nil, err
		}
		return daemon.Dial(paths.Socket)
	}
	token := strings.TrimSpace(flags.Token)
	if token == "" {
		token, _ = remote.TokenFromEnv() // best effort; DialRemote rejects an empty token
	}
	return remote.DialRemote(remote.RemoteConfig{
		Address:    flags.Addr,
		Token:      token,
		CACertFile: flags.CACert,
		ServerName: flags.ServerName,
	})
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// daemonReachable reports whether a daemon is accepting connections (a successful
// handshake). Used for the single-instance check and start-up wait.
func daemonReachable(paths daemon.Paths) bool {
	client, err := daemon.Dial(paths.Socket)
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
}

func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}
