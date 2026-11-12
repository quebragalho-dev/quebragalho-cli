package cli

import (
	"context"
	"sync"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const (
	optionalMCPPromptGrace = time.Second
	optionalMCPCloseGrace  = 250 * time.Millisecond
)

// splitMCPStartupConfig keeps explicitly configured servers on the
// prompt-critical path. Only unchanged built-in defaults may initialize after
// first paint; their failure was already intentionally silent.
func splitMCPStartupConfig(cfg config.MCPConfig) (critical config.MCPConfig, optional config.MCPConfig) {
	critical.Servers = map[string]config.MCPServerConfig{}
	optional.Servers = map[string]config.MCPServerConfig{}
	for name, server := range cfg.Servers {
		if server.Disabled {
			continue
		}
		if config.IsUnconfiguredDefault(name, server) {
			optional.Servers[name] = server
			continue
		}
		critical.Servers[name] = server
	}
	return critical, optional
}

type optionalMCPStartup struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	runtime mcpToolRuntime

	closeOnce        sync.Once
	runtimeCloseOnce sync.Once
	closeErr         error
}

func startOptionalMCP(
	parent context.Context,
	registry *tools.Registry,
	cfg config.MCPConfig,
	options mcp.RegisterOptions,
	register func(context.Context, *tools.Registry, config.MCPConfig, mcp.RegisterOptions) (mcpToolRuntime, error),
	onReady func(),
) *optionalMCPStartup {
	ctx, cancel := context.WithCancel(parent)
	startup := &optionalMCPStartup{cancel: cancel, done: make(chan struct{})}
	go func() {
		runtime, err := register(ctx, registry, cfg, options)
		if err == nil && onReady != nil {
			onReady()
		}
		startup.mu.Lock()
		startup.runtime = runtime
		startup.mu.Unlock()
		close(startup.done)
	}()
	return startup
}

// Await gives optional tools one bounded opportunity to join the next turn's
// immutable registry snapshot. A timeout leaves this turn on the existing core
// tool set; a later turn observes the completed generation.
func (startup *optionalMCPStartup) Await(ctx context.Context, timeout time.Duration) bool {
	if startup == nil {
		return true
	}
	select {
	case <-startup.done:
		return true
	default:
	}
	if timeout <= 0 {
		select {
		case <-startup.done:
			return true
		case <-ctx.Done():
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-startup.done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (startup *optionalMCPStartup) Close() error {
	if startup == nil {
		return nil
	}
	startup.closeOnce.Do(func() {
		startup.cancel()
		reaped := make(chan struct{})
		go func() {
			defer close(reaped)
			<-startup.done
			startup.closeRuntime()
		}()
		timer := time.NewTimer(optionalMCPCloseGrace)
		defer timer.Stop()
		select {
		case <-reaped:
		case <-timer.C:
		}
	})
	startup.mu.Lock()
	defer startup.mu.Unlock()
	return startup.closeErr
}

func (startup *optionalMCPStartup) closeRuntime() {
	startup.runtimeCloseOnce.Do(func() {
		startup.mu.Lock()
		runtime := startup.runtime
		startup.mu.Unlock()
		if runtime == nil {
			return
		}
		err := runtime.Close()
		startup.mu.Lock()
		if err != nil && startup.closeErr == nil {
			startup.closeErr = err
		}
		startup.mu.Unlock()
	})
}

func (startup *optionalMCPStartup) Skipped() []mcp.SkippedServer {
	if startup == nil {
		return nil
	}
	startup.mu.Lock()
	defer startup.mu.Unlock()
	if startup.runtime == nil {
		return nil
	}
	return startup.runtime.Skipped()
}

// Status reports the optional runtime's server states once it settled, or
// nil while it is still connecting. It lets /mcp show optional defaults
// (like keyless Exa) with their failure reasons instead of hiding them.
func (startup *optionalMCPStartup) Status() []mcp.ServerStatus {
	if startup == nil {
		return nil
	}
	startup.mu.Lock()
	defer startup.mu.Unlock()
	runtime, ok := startup.runtime.(*mcp.Runtime)
	if !ok || runtime == nil {
		return nil
	}
	return runtime.Status()
}

// ReconnectServer reconnects one server of the optional runtime, if it owns
// the name. An unknown name returns *mcp.UnknownServerError so the caller
// can try the next runtime.
func (startup *optionalMCPStartup) ReconnectServer(ctx context.Context, serverName string) error {
	if startup == nil {
		return &mcp.UnknownServerError{Name: serverName}
	}
	startup.mu.Lock()
	runtime, _ := startup.runtime.(*mcp.Runtime)
	startup.mu.Unlock()
	if runtime == nil {
		return &mcp.UnknownServerError{Name: serverName}
	}
	return runtime.Reconnect(ctx, serverName)
}
