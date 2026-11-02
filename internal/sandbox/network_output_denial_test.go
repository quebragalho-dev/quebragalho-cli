package sandbox

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

func TestOutputIndicatesBlockedNetworkPositives(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"python hostname DNS", "urllib.error.URLError: <urlopen error [Errno 8] nodename nor servname provided, or not known>"},
		{"nc hostname DNS", "nc: getaddrinfo: nodename nor servname provided"},
		{"node getaddrinfo ENOTFOUND", "Error: getaddrinfo ENOTFOUND example.com"},
		{"getaddrinfo EAI_NONAME", "socket.gaierror: getaddrinfo EAI_NONAME example.com"},
		{"curl could not resolve", "curl: (6) Could not resolve host: example.com"},
		{"glibc temporary failure", "ping: example.com: Temporary failure in name resolution"},
		{"python IP EPERM urlopen", "urllib.error.URLError: <urlopen error [Errno 1] Operation not permitted>"},
		{"connect operation not permitted", "socket.create_connection failed: connect: Operation not permitted"},
		{"socket operation not permitted", "socket failed: [Errno 1] Operation not permitted in socket call"},
		{"node connect EPERM", "Error: connect EPERM 93.184.216.34:80"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !OutputIndicatesBlockedNetwork(tc.output) {
				t.Fatalf("OutputIndicatesBlockedNetwork(%q) = false, want true", tc.output)
			}
			if denial := NetworkDenialFromOutput(tc.output); denial == nil {
				t.Fatalf("NetworkDenialFromOutput(%q) = nil, want denial", tc.output)
			} else {
				if denial.Capability.Kind != execution.CapabilityExternalNetwork {
					t.Fatalf("capability = %q, want external_network", denial.Capability.Kind)
				}
				if denial.Source != execution.DenialSourcePlatformSandbox {
					t.Fatalf("source = %q, want platform_sandbox", denial.Source)
				}
				if !denial.Recoverable || denial.NextAction != execution.DenialNextActionRequestApproval {
					t.Fatalf("denial must be recoverable by approval, got %#v", denial)
				}
			}
		})
	}
}

func TestOutputIndicatesBlockedNetworkNegatives(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"empty grep no result", ""},
		{"grep no result text", "exit_code: 1"},
		{"file permission denied", "cat: /etc/shadow: Permission denied"},
		{"chmod EPERM without connect", "chmod: /foo/bar: Operation not permitted"},
		{"open EPERM without connect", `open("/foo/bar"): Operation not permitted`},
		{"bare EPERM without connect", "Error: EPERM"},
		{"connect without EPERM", "connect: connection refused"},
		{"socket without EPERM", "socket: connection reset"},
		{"urlopen without EPERM", "urlopen error: timeout"},
		{"getaddrinfo without code", "getaddrinfo failed for example.com"},
		{"ENOTFOUND without getaddrinfo", "Error: ENOTFOUND example.com"},
		{"EAI_NONAME without getaddrinfo", "Error: EAI_NONAME example.com"},
		{"go test failure", "--- FAIL: TestFoo (0.00s)\nFAIL"},
		{"go build error", "build failed: undefined: foo"},
		{"bare network word", "I love the word network in a sentence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if OutputIndicatesBlockedNetwork(tc.output) {
				t.Fatalf("OutputIndicatesBlockedNetwork(%q) = true, want false", tc.output)
			}
			if denial := NetworkDenialFromOutput(tc.output); denial != nil {
				t.Fatalf("NetworkDenialFromOutput(%q) = %#v, want nil", tc.output, denial)
			}
		})
	}
}

func TestNetworkOutputFallbackDenial(t *testing.T) {
	signature := "urllib.error.URLError: <urlopen error [Errno 8] nodename nor servname provided, or not known>"
	eligible := func() (bool, BackendName, NetworkMode, string, string) {
		return true, BackendMacOSSeatbelt, NetworkDeny, "qg-sandbox-denied-v1-1-1", "darwin"
	}
	t.Run("signature without tag offers denial", func(t *testing.T) {
		wrapped, backend, mode, tag, goos := eligible()
		denial := NetworkOutputFallbackDenial(1, false, wrapped, backend, mode, tag, goos, signature)
		if denial == nil {
			t.Fatal("want denial for failed deny-network run without tag and with signature, got nil")
		}
		if denial.Capability.Kind != execution.CapabilityExternalNetwork {
			t.Fatalf("capability = %q, want external_network", denial.Capability.Kind)
		}
	})
	t.Run("no signature stays raw", func(t *testing.T) {
		wrapped, backend, mode, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, wrapped, backend, mode, tag, goos, "exit_code: 1"); denial != nil {
			t.Fatalf("want nil without signature, got %#v", denial)
		}
	})
	t.Run("exit zero never prompts", func(t *testing.T) {
		wrapped, backend, mode, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(0, false, wrapped, backend, mode, tag, goos, signature); denial != nil {
			t.Fatalf("want nil for exit 0, got %#v", denial)
		}
	})
	t.Run("tag seen never falls back", func(t *testing.T) {
		wrapped, backend, mode, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, true, wrapped, backend, mode, tag, goos, signature); denial != nil {
			t.Fatalf("want nil when tag already seen, got %#v", denial)
		}
	})
	t.Run("non-darwin never falls back", func(t *testing.T) {
		wrapped, backend, mode, tag, _ := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, wrapped, backend, mode, tag, "linux", signature); denial != nil {
			t.Fatalf("want nil off macOS, got %#v", denial)
		}
	})
	t.Run("unwrapped never falls back", func(t *testing.T) {
		_, backend, mode, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, false, backend, mode, tag, goos, signature); denial != nil {
			t.Fatalf("want nil when unwrapped, got %#v", denial)
		}
	})
	t.Run("non-seatbelt never falls back", func(t *testing.T) {
		wrapped, _, mode, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, wrapped, BackendLinuxBwrap, mode, tag, goos, signature); denial != nil {
			t.Fatalf("want nil off seatbelt, got %#v", denial)
		}
	})
	t.Run("allow network never falls back", func(t *testing.T) {
		wrapped, backend, _, tag, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, wrapped, backend, NetworkAllow, tag, goos, signature); denial != nil {
			t.Fatalf("want nil for allow-network plan, got %#v", denial)
		}
	})
	t.Run("empty tag never falls back", func(t *testing.T) {
		wrapped, backend, mode, _, goos := eligible()
		if denial := NetworkOutputFallbackDenial(1, false, wrapped, backend, mode, "", goos, signature); denial != nil {
			t.Fatalf("want nil without monitor tag, got %#v", denial)
		}
	})
}
