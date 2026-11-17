package mcp

import "context"

// Connect builds a ToolClient with default options. Test-only: production
// connects through ConnectWithOptions with explicit execution and workspace
// roots, so the bare default never runs outside tests.
func Connect(ctx context.Context, server Server) (ToolClient, error) {
	return ConnectWithOptions(ctx, server, ConnectOptions{})
}
