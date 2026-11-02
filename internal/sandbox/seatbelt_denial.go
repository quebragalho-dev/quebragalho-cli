package sandbox

import (
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

// NetworkDenialFromBlocks inspects attributed seatbelt denial messages for a
// network operation and builds the structured denial the agent loop uses to
// offer a network-permission retry. It returns nil when no block shows a
// network denial. Classification reads the kernel's operation token (e.g.
// "network-outbound"), never command output.
func NetworkDenialFromBlocks(blocks []string) *execution.Denial {
	for _, block := range blocks {
		if op := seatbeltDenyOperation(block); strings.HasPrefix(op, "network") {
			return &execution.Denial{
				Capability:  execution.Capability{Kind: execution.CapabilityExternalNetwork},
				Source:      execution.DenialSourcePlatformSandbox,
				Reason:      "sandbox blocked network access (" + op + ")",
				Recoverable: true,
				NextAction:  execution.DenialNextActionRequestApproval,
			}
		}
	}
	return nil
}

// seatbeltDenyOperation extracts the operation token from an attributed
// denial message ("Sandbox: Python(1) deny(1) network-outbound ..." ->
// "network-outbound"), or "" when the message carries no deny operation.
func seatbeltDenyOperation(block string) string {
	fields := strings.Fields(block)
	for i, field := range fields {
		if field == "deny" || strings.HasPrefix(field, "deny(") {
			if i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}
