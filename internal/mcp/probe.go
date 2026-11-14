package mcp

import (
	"context"
	"sync"
)

// ProbeResult reports one server's live status for status displays: how many
// tools answered, or why the server could not be reached.
type ProbeResult struct {
	ToolCount int
	Err       error
}

// ProbeServers connects to each server once (no retry) and lists its tools,
// closing every client it opened. Each probe is bounded by the connect budget
// so one dead server cannot stall a status display. Spawning stdio servers is
// the caller's responsibility to gate behind workspace trust.
func ProbeServers(ctx context.Context, servers []Server, factory func(context.Context, Server) (ToolClient, error)) map[string]ProbeResult {
	if factory == nil {
		factory = func(ctx context.Context, server Server) (ToolClient, error) {
			return ConnectWithOptions(ctx, server, ConnectOptions{})
		}
	}
	results := make(map[string]ProbeResult, len(servers))
	if len(servers) == 0 {
		return results
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server Server) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, connectTimeout())
			defer cancel()
			client, remote, err := connectAndList(probeCtx, factory, server)
			if err == nil {
				_ = client.Close()
			}
			mu.Lock()
			results[server.Name] = ProbeResult{ToolCount: len(remote), Err: redactError(server, err)}
			mu.Unlock()
		}(server)
	}
	wg.Wait()
	return results
}
