package sandbox

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

func TestNetworkDenialFromBlocks(t *testing.T) {
	network := "Sandbox: Python(87035) deny(1) network-outbound /private/var/run/mDNSResponder"
	for _, tc := range []struct {
		name   string
		blocks []string
		want   bool
	}{
		{"network denial attributed", []string{network}, true},
		{"tag-only network hit", []string{
			"Sandbox: deny network (network access blocked by sandbox)",
		}, true},
		{"network denial among file denials", []string{
			"Sandbox: python3(87035) deny(1) file-read-data /",
			network,
		}, true},
		{"file denial only", []string{
			"Sandbox: bash(123) deny(1) file-write-create /etc/x",
		}, false},
		{"empty", nil, false},
		{"non sandbox line", []string{"some unrelated log line"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NetworkDenialFromBlocks(tc.blocks)
			if tc.want && got == nil {
				t.Fatal("want a network denial, got nil")
			}
			if !tc.want && got != nil {
				t.Fatalf("want no denial, got %#v", got)
			}
			if tc.want {
				if got.Capability.Kind != execution.CapabilityExternalNetwork {
					t.Fatalf("capability = %q, want external_network", got.Capability.Kind)
				}
				if got.Source != execution.DenialSourcePlatformSandbox {
					t.Fatalf("source = %q, want platform_sandbox", got.Source)
				}
				if !got.Recoverable || got.NextAction != execution.DenialNextActionRequestApproval {
					t.Fatalf("denial must be recoverable by approval, got %#v", got)
				}
			}
		})
	}
}
