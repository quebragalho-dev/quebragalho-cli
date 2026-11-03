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
		{"go lookup via resolver no such host", "dial tcp: lookup proxy.golang.org on 192.168.1.1:53: no such host"},
		{"go mod lookup no such host", "go: rsc.io/quote@v1.5.2: Get \"https://proxy.golang.org/rsc.io/quote/@v/v1.5.2.info\": dial tcp: lookup proxy.golang.org: no such host"},
		{"python IP EPERM urlopen", "urllib.error.URLError: <urlopen error [Errno 1] Operation not permitted>"},
		{"connect operation not permitted", "socket.create_connection failed: connect: Operation not permitted"},
		{"go dial tcp connect EPERM", "go: downloading example.com: dial tcp 93.184.216.34:443: connect: operation not permitted"},
		{"python raw socket dot-connect EPERM", "File \"/opt/homebrew/Cellar/python@3.14/3.14.6/Frameworks/Python.framework/Versions/3.14/lib/python3.14/socket.py\", line 859, in create_connection\n    sock.connect(sa)\nPermissionError: [Errno 1] Operation not permitted"},
		{"node connect EPERM", "Error: connect EPERM 93.184.216.34:80"},
		{"node syscall connect EPERM", "Error: EPERM: operation not permitted, connect 93.184.216.34:443\n    at TCPConnectWrap.afterConnect [as oncomplete]\n  errno: -4048,\n  code: 'EPERM',\n  syscall: 'connect',\n  address: '93.184.216.34',\n  port: 443"},
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
		{"write touch socket filename is not network", "touch: /etc/crivo-socket-probe: Operation not permitted"},
		{"write mkdir connector dirname is not network", "mkdir: /Users/Shared/crivo-connector-cache: Operation not permitted"},
		{"write python open socket path is not network", "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\n    open('/etc/crivo-socket.conf','w')\nPermissionError: [Errno 1] Operation not permitted: '/etc/crivo-socket.conf'"},
		{"write python write_text connection path is not network", "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\n    import pathlib; pathlib.Path('/Users/Shared/db_connection.py').write_text('x')\nPermissionError: [Errno 1] Operation not permitted: '/Users/Shared/db_connection.py'"},
		{"write node open syscall is not network", "Error: EPERM: operation not permitted, open '/etc/ws-connect.json'\n    at Object.writeFileSync (node:fs:2398:20)\n  errno: -1,\n  code: 'EPERM',\n  syscall: 'open',\n  path: '/etc/ws-connect.json'"},
		{"bare EPERM without connect", "Error: EPERM"},
		{"connect without EPERM", "connect: connection refused"},
		{"dot-connect without EPERM stays raw", "sock.connect(sa): connection refused"},
		{"socket without EPERM", "socket: connection reset"},
		{"urlopen without errno", "urlopen error: timeout"},
		{"lookup without no-such-host stays raw", "go: proxy.golang.org: dial tcp: lookup proxy.golang.org: connection refused"},
		{"no-such-host without lookup stays raw", "Error: no such host"},
		{"python KeyError through client.connect", "Traceback (most recent call last):\n  File \"db_app.py\", line 10, in <module>\n    client.connect()\n  File \"db_app.py\", line 6, in connect\n    return self.cfg[\"filePermission\"]\nKeyError: 'filePermission'"},
		{"python file write inside connect method", "    Client().connect()\n  File \"lock_app.py\", line 3, in connect\n    with open(\"/Users/Shared/qg-crivo-db.lock\", \"w\") as fh:\nPermissionError: [Errno 1] Operation not permitted: '/Users/Shared/qg-crivo-db.lock'"},
		{"go test lookup and no such host on unrelated lines", "--- FAIL: TestLookup (0.00s)\n    lk_test.go:6: resolver stub: no such host alias configured\n    lk_test.go:7: want 2 records, got 0"},
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
