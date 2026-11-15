package mcp

import (
	"context"
	"fmt"
	"strings"
)

// ServerState is the lifecycle state of one configured MCP server, in
// the style the /mcp manager renders.
type ServerState string

const (
	// ServerStateConnected serves tools normally.
	ServerStateConnected ServerState = "connected"
	// ServerStateNeedsAuth requires an OAuth login before it can connect.
	ServerStateNeedsAuth ServerState = "needs_auth"
	// ServerStateFailed did not connect or list; Reason says why.
	ServerStateFailed ServerState = "failed"
	// ServerStateDisabled is configured but switched off; it stays listed so
	// /mcp can turn it back on.
	ServerStateDisabled ServerState = "disabled"
)

// ServerStatus is one row of the /mcp panel: the server's state, the failure
// reason when it failed, and how many tools it serves when connected.
type ServerStatus struct {
	Name      string
	State     ServerState
	Reason    string
	ToolCount int
}

// UnknownServerError reports a Reconnect for a server the runtime never
// registered. The caller can try another runtime (critical vs optional).
type UnknownServerError struct {
	Name string
}

func (err *UnknownServerError) Error() string {
	return fmt.Sprintf("unknown MCP server %q", err.Name)
}

// Status reports every known server: connected with its tool count, failed
// with its reason, needs-auth for OAuth servers without a usable token, and
// disabled servers still listed so they can be re-enabled.
func (runtime *Runtime) Status() []ServerStatus {
	if runtime == nil {
		return nil
	}
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	skippedByName := make(map[string]SkippedServer, len(runtime.skipped))
	for _, skipped := range runtime.skipped {
		skippedByName[skipped.Name] = skipped
	}
	status := make([]ServerStatus, 0, len(runtime.servers)+len(runtime.disabled))
	for _, server := range runtime.servers {
		switch runtime.membershipLocked(server.Name) {
		case memberDisabled:
			status = append(status, ServerStatus{Name: server.Name, State: ServerStateDisabled})
		case memberSkipped:
			skipped, ok := skippedByName[server.Name]
			reason := ""
			if ok && skipped.Err != nil {
				reason = sanitizeMCPErrorText(server.Redact(skipped.Err.Error()))
			}
			state := ServerStateFailed
			if serverRequiresOAuth(server) {
				state = ServerStateNeedsAuth
			}
			status = append(status, ServerStatus{Name: server.Name, State: state, Reason: reason})
		case memberNeedsAuth:
			status = append(status, ServerStatus{Name: server.Name, State: ServerStateNeedsAuth})
		case memberLive, memberClosed:
			status = append(status, ServerStatus{Name: server.Name, State: ServerStateConnected, ToolCount: len(runtime.serverTools[server.Name])})
		}
	}
	for name := range runtime.disabled {
		found := false
		for _, server := range runtime.servers {
			if server.Name == name {
				found = true
				break
			}
		}
		if !found {
			status = append(status, ServerStatus{Name: name, State: ServerStateDisabled})
		}
	}
	return status
}

func serverRequiresOAuth(server Server) bool {
	return strings.EqualFold(strings.TrimSpace(server.Auth), ServerAuthOAuth) || server.OAuth != nil
}

func hasUsableOAuthToken(server Server) bool {
	store, err := NewTokenStore(TokenStoreOptions{})
	if err != nil {
		return false
	}
	token, ok, err := store.LoadForServer(server)
	if err != nil || !ok {
		return false
	}
	return strings.TrimSpace(token.AccessToken) != ""
}

// Reconnect forces one server to reconnect now (new initialize) and rebuilds
// its tools, so the /mcp "reconectar" action recovers a failed server without
// restarting qg. Only the snapshot and the commit take the catalog lock; the
// connect itself runs unlocked so a slow dial never freezes the panel.
// On failure the previous state is kept and the error — with the fresh
// reason — is returned.
func (runtime *Runtime) Reconnect(ctx context.Context, serverName string) error {
	if runtime == nil {
		return nil
	}
	runtime.refreshMu.Lock()
	if runtime.closed {
		runtime.refreshMu.Unlock()
		return fmt.Errorf("MCP runtime is closed")
	}
	var server *Server
	for index := range runtime.servers {
		if runtime.servers[index].Name == serverName {
			snapshot := runtime.servers[index]
			server = &snapshot
			break
		}
	}
	disabled := runtime.disabled[serverName]
	if server == nil {
		runtime.refreshMu.Unlock()
		if disabled {
			return disabledServerError(serverName)
		}
		return &UnknownServerError{Name: serverName}
	}
	// The membership table owns the reconnect policy: a disabled server
	// never dials, everything else reachable here may.
	if _, ok := legalMemberTransition(runtime.membershipLocked(serverName), memberEventReconnect); !ok {
		runtime.refreshMu.Unlock()
		return disabledServerError(serverName)
	}
	runtime.refreshMu.Unlock()
	fresh, remote, err := connectWithRetry(ctx, runtime.factory, *server, connectTimeout())
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	if runtime.closed {
		if fresh != nil {
			_ = fresh.Close()
		}
		return fmt.Errorf("MCP runtime is closed")
	}
	present := false
	for index := range runtime.servers {
		if runtime.servers[index].Name == serverName {
			present = true
			break
		}
	}
	if !present {
		if fresh != nil {
			_ = fresh.Close()
		}
		return &UnknownServerError{Name: serverName}
	}
	if err != nil {
		err = redactError(*server, err)
		runtime.replaceSkippedLocked(SkippedServer{Name: serverName, Err: err})
		return err
	}
	if err := runtime.adoptRelistLocked(*server, fresh, remote); err != nil {
		err = redactError(*server, err)
		runtime.replaceSkippedLocked(SkippedServer{Name: serverName, Err: err})
		return err
	}
	runtime.dropSkippedLocked(serverName)
	return nil
}

func (runtime *Runtime) replaceSkippedLocked(entry SkippedServer) {
	kept := runtime.skipped[:0]
	for _, skipped := range runtime.skipped {
		if skipped.Name != entry.Name {
			kept = append(kept, skipped)
		}
	}
	runtime.skipped = append(kept, entry)
}

func (runtime *Runtime) dropSkippedLocked(serverName string) {
	kept := runtime.skipped[:0]
	for _, skipped := range runtime.skipped {
		if skipped.Name != serverName {
			kept = append(kept, skipped)
		}
	}
	runtime.skipped = kept
}
