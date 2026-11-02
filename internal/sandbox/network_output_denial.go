package sandbox

import (
	"runtime"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

// OutputIndicatesBlockedNetwork reports whether combined command output
// carries an unequivocal blocked-network signature. It only matches short,
// tested DNS and connect/socket failures; a bare "network" or a file
// "permission denied" never matches.
func OutputIndicatesBlockedNetwork(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "nodename nor servname provided") {
		return true
	}
	if strings.Contains(lower, "could not resolve host") {
		return true
	}
	if strings.Contains(lower, "temporary failure in name resolution") {
		return true
	}
	if strings.Contains(lower, "getaddrinfo") &&
		(strings.Contains(lower, "enotfound") || strings.Contains(lower, "eai_noname")) {
		return true
	}
	hasEPERM := strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "eperm")
	if hasEPERM &&
		(strings.Contains(lower, "connect") || strings.Contains(lower, "socket") || strings.Contains(lower, "urlopen")) {
		return true
	}
	return false
}

// NetworkDenialFromOutput builds the network denial the agent loop turns into
// a permission retry when output carries a blocked-network signature. It
// returns nil otherwise. The denial matches NetworkDenialFromBlocks.
func NetworkDenialFromOutput(output string) *execution.Denial {
	if !OutputIndicatesBlockedNetwork(output) {
		return nil
	}
	return &execution.Denial{
		Capability:  execution.Capability{Kind: execution.CapabilityExternalNetwork},
		Source:      execution.DenialSourcePlatformSandbox,
		Reason:      "sandbox blocked network access",
		Recoverable: true,
		NextAction:  execution.DenialNextActionRequestApproval,
	}
}

// IsNetworkOutputFallbackEligible reports whether a plan may offer the output
// fallback: macOS, wrapped, deny-network, tagged. The goos parameter keeps the
// gate testable off macOS; callers pass runtime.GOOS.
func IsNetworkOutputFallbackEligible(wrapped bool, targetBackend BackendName, networkMode NetworkMode, monitorTag string, goos string) bool {
	if goos != "darwin" {
		return false
	}
	if !wrapped {
		return false
	}
	if targetBackend != BackendMacOSSeatbelt {
		return false
	}
	if NormalizeNetworkMode(networkMode) != NetworkDeny {
		return false
	}
	if strings.TrimSpace(monitorTag) == "" {
		return false
	}
	return true
}

// EligibleForNetworkOutputFallback reports whether this plan may offer the
// output fallback.
func (plan CommandPlan) EligibleForNetworkOutputFallback() bool {
	return IsNetworkOutputFallbackEligible(plan.Wrapped, plan.TargetBackend, plan.PermissionProfile.Network.Mode, plan.MonitorTag, runtime.GOOS)
}

// NetworkOutputFallbackDenial returns the output-based network denial for a
// failed deny-network run without a tag hit, or nil. No extra wait: callers
// invoke it synchronously after AwaitTag with the captured output.
func NetworkOutputFallbackDenial(exitCode int, tagSeen bool, wrapped bool, targetBackend BackendName, networkMode NetworkMode, monitorTag string, goos string, output string) *execution.Denial {
	if exitCode == 0 {
		return nil
	}
	if tagSeen {
		return nil
	}
	if !IsNetworkOutputFallbackEligible(wrapped, targetBackend, networkMode, monitorTag, goos) {
		return nil
	}
	return NetworkDenialFromOutput(output)
}
