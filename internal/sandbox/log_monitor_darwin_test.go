//go:build darwin

package sandbox

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseSandboxDenyLine(t *testing.T) {
	tag := sandboxDenialLogTag
	cases := []struct {
		name   string
		line   string
		wantOK bool
	}{
		{"denial with tag", `2026-06-13 Sandbox: bash(123) deny(1) file-write-create /etc/x ` + tag, true},
		{"denial without tag", `2026-06-13 Sandbox: bash(123) deny(1) file-write-create /etc/x`, false},
		{"not a sandbox line", `2026-06-13 some unrelated log ` + tag, false},
		{"noisy daemon", `2026-06-13 Sandbox: mDNSResponder(9) deny(1) network-outbound ` + tag, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := parseSandboxDenyLine(tc.line, tag)
			if ok != tc.wantOK {
				t.Fatalf("parseSandboxDenyLine(%q) ok=%v, want %v (msg=%q)", tc.line, ok, tc.wantOK, msg)
			}
			if ok {
				if strings.TrimSpace(msg) == "" {
					t.Fatalf("a matched denial must yield a non-empty message")
				}
				if strings.Contains(msg, tag) {
					t.Fatalf("the tag must be stripped from the reported message: %q", msg)
				}
				if !strings.Contains(msg, "Sandbox:") {
					t.Fatalf("the message should start at the Sandbox: marker: %q", msg)
				}
			}
		})
	}
}

func TestDenialMonitorEmptyTagIsNoop(t *testing.T) {
	monitor := StartDenialMonitor(t.Context(), "")
	if got := monitor.Stop(); got != nil {
		t.Fatalf("a no-op monitor must return no blocks, got %#v", got)
	}
	if monitor.AwaitTag() {
		t.Fatal("a no-op monitor must never report a tag")
	}
	narrow := StartNetworkDenialMonitor(t.Context(), "")
	if got := narrow.Stop(); got != nil {
		t.Fatalf("a no-op narrow monitor must return no blocks, got %#v", got)
	}
	if narrow.AwaitTag() {
		t.Fatal("a no-op narrow monitor must never report a tag")
	}
}

func TestNarrowObserve(t *testing.T) {
	netTag := "qg-sandbox-denied-v1-1-2-net"
	cases := []struct {
		name   string
		line   string
		wantOK bool
	}{
		{"tag hit", `2026-11-02 E kernel[0:1] [com.apple.sandbox.reporting:violation] ` + netTag, true},
		{"bare tag hit", netTag, true},
		{"header skipped", `Filtering the log data using "composedMessage CONTAINS \"` + netTag + `\""`, false},
		{"plain tag without suffix is not a hit", `2026-11-02 E kernel[0:1] qg-sandbox-denied-v1-1-2`, false},
		{"longer sequence tag line is not a hit", `2026-11-02 E kernel[0:1] [com.apple.sandbox.reporting:violation] qg-sandbox-denied-v1-1-20-net`, false},
		{"unrelated line", `2026-11-02 E cfprefsd[1:2] Could not open <private>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, ok := narrowObserve(tc.line, netTag)
			if ok != tc.wantOK {
				t.Fatalf("narrowObserve(%q) ok=%v, want %v", tc.line, ok, tc.wantOK)
			}
			if ok && NetworkDenialFromBlocks([]string{block}) == nil {
				t.Fatalf("narrow block %q must classify as a network denial", block)
			}
		})
	}
}

func TestContainsTagToken(t *testing.T) {
	tag := "qg-sandbox-denied-v1-99-1"
	cases := []struct {
		name   string
		line   string
		wantOK bool
	}{
		{"bare tag", tag, true},
		{"tag with trailing space", tag + " ", true},
		{"tag with trailing quote", tag + `"`, true},
		{"own network tag", tag + "-net", true},
		{"own network tag trailing space", tag + "-net ", true},
		{"seq 10 tag is not seq 1", "qg-sandbox-denied-v1-99-10", false},
		{"seq 10 network tag is not seq 1", "qg-sandbox-denied-v1-99-10-net", false},
		{"seq 11 network tag is not seq 1", "qg-sandbox-denied-v1-99-11-net", false},
		{"seq 100 tag is not seq 10", "qg-sandbox-denied-v1-99-100", false},
		{"unrelated line", `2026-11-02 E kernel[0:1] Sandbox: bash(1) deny(1) file-write`, false},
		{"tag as suffix of longer token", "X" + tag, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsTagToken(tc.line, tag); got != tc.wantOK {
				t.Fatalf("containsTagToken(%q) = %v, want %v", tc.line, got, tc.wantOK)
			}
		})
	}
	if containsTagToken("qg-sandbox-denied-v1-99-110-net", "qg-sandbox-denied-v1-99-11") {
		t.Fatal("seq-11 tag must not match a seq-110 network tag")
	}
	if !containsTagToken("qg-sandbox-denied-v1-99-11-net", "qg-sandbox-denied-v1-99-11") {
		t.Fatal("seq-11 tag must match its own network tag")
	}
	if containsTagToken("anything", "") {
		t.Fatal("an empty tag must never match")
	}
}
func TestDenialAttributorPairsTagWithViolation(t *testing.T) {
	tag := "qg-sandbox-denied-v1-9-9"
	// Real shape: the violation line carries no tag; the tag follows on its
	// own line. Interleaved noise must not break the pairing.
	lines := []string{
		`2026-11-02 01:34:01.217 E  kernel[0:18c8cb9] [com.apple.sandbox.reporting:violation] Sandbox: python3(87035) deny(1) mach-lookup com.apple.system.notification_center`,
		`2026-11-02 01:34:01.222 E  cfprefsd[75504:18c65b4] [com.apple.defaults:cfprefsd] Could not open <private> due to Too many open files`,
		`2026-11-02 01:34:01.402 E  kernel[0:18c8cb9] [com.apple.sandbox.reporting:violation] Sandbox: Python(87035) deny(1) network-outbound /private/var/run/mDNSResponder`,
		tag,
	}
	attributor := newDenialAttributor(tag)
	var attributed []string
	for _, line := range lines {
		if msg, ok := attributor.observe(line); ok {
			attributed = append(attributed, msg)
		}
	}
	if len(attributed) != 1 {
		t.Fatalf("attributed %d violations, want exactly the network one: %q", len(attributed), attributed)
	}
	if !strings.Contains(attributed[0], "network-outbound") {
		t.Fatalf("attributed the wrong violation: %q", attributed[0])
	}
	if strings.Contains(attributed[0], tag) {
		t.Fatalf("attributed message must not carry the tag: %q", attributed[0])
	}
}

func TestDenialAttributorIgnoresUntaggedNoise(t *testing.T) {
	attributor := newDenialAttributor("qg-sandbox-denied-v1-1-1")
	// A tag line with no preceding violation attributes nothing.
	if msg, ok := attributor.observe("qg-sandbox-denied-v1-1-1"); ok {
		t.Fatalf("bare tag with no violation must not attribute, got %q", msg)
	}
	// A violation without a following tag stays pending, never reported.
	if msg, ok := attributor.observe(`2026-11-02 01:34:01 E kernel[0:1] [com.apple.sandbox.reporting:violation] Sandbox: bash(123) deny(1) file-write-create /etc/x`); ok {
		t.Fatalf("violation without tag must stay pending, got %q", msg)
	}
	// A noisier daemon's own chatter never attributes, even with its tag line.
	other := newDenialAttributor("other-tag")
	if msg, ok := other.observe(`2026-11-02 01:34:01 E kernel[0:1] [com.apple.sandbox.reporting:violation] Sandbox: mDNSResponder(9) deny(1) network-outbound /private/var/run/mDNSResponder`); ok {
		t.Fatalf("noisy daemon must be filtered, got %q", msg)
	}
	if msg, ok := other.observe("other-tag"); ok {
		t.Fatalf("filtered violation must not attribute on tag, got %q", msg)
	}
}

func TestDenialAttributorKeepsLatestPending(t *testing.T) {
	attributor := newDenialAttributor("qg-sandbox-denied-v1-2-2")
	attributor.observe(`2026-11-02 01:34:01 E kernel[0:1] [com.apple.sandbox.reporting:violation] Sandbox: bash(123) deny(1) file-write-create /etc/x`)
	attributor.observe(`2026-11-02 01:34:02 E kernel[0:1] [com.apple.sandbox.reporting:violation] Sandbox: Python(87035) deny(1) network-outbound /private/var/run/mDNSResponder`)
	got, ok := attributor.observe("qg-sandbox-denied-v1-2-2")
	if !ok || !strings.Contains(got, "network-outbound") {
		t.Fatalf("tag must attribute the latest violation, got %q, %v", got, ok)
	}
}

// TestDenialAttributorIgnoresLongerSequenceTags replays two concurrent runs
// whose sequence numbers share a prefix (1 vs 10, 11 vs 110): the shorter
// run must never attribute the longer run's network denial.
func TestDenialAttributorIgnoresLongerSequenceTags(t *testing.T) {
	pairs := [][2]int{{1, 10}, {11, 110}}
	for _, pair := range pairs {
		short, long := pair[0], pair[1]
		shortTag := "qg-sandbox-denied-v1-77-" + strconv.Itoa(short)
		longTag := "qg-sandbox-denied-v1-77-" + strconv.Itoa(long)
		// Real narrow-feed shape: the violation line carries no tag, the
		// network tag of the longer run follows on its own line.
		lines := []string{
			`2026-11-02 01:34:01 E kernel[0:1] [com.apple.sandbox.reporting:violation] Sandbox: Python(87035) deny(1) network-outbound remote:*:1`,
			sandboxNetworkDenialTag(longTag),
		}
		shortAttributor := newDenialAttributor(shortTag)
		for _, line := range lines {
			if msg, ok := shortAttributor.observe(line); ok {
				t.Fatalf("seq-%d attributor must ignore seq-%d lines, attributed %q", short, long, msg)
			}
		}
		longAttributor := newDenialAttributor(longTag)
		var attributed []string
		for _, line := range lines {
			if msg, ok := longAttributor.observe(line); ok {
				attributed = append(attributed, msg)
			}
		}
		if len(attributed) != 1 || !strings.Contains(attributed[0], "network-outbound") {
			t.Fatalf("seq-%d attributor must attribute its own denial, got %q", long, attributed)
		}
		// The narrow feed classifies on the tag line alone: the short run's
		// net tag must not fire on the long run's tag line either.
		if _, ok := narrowObserve(sandboxNetworkDenialTag(longTag), sandboxNetworkDenialTag(shortTag)); ok {
			t.Fatalf("seq-%d net tag must not match the seq-%d tag line", short, long)
		}
		if _, ok := narrowObserve(sandboxNetworkDenialTag(longTag), sandboxNetworkDenialTag(longTag)); !ok {
			t.Fatalf("seq-%d net tag must match its own tag line", long)
		}
	}
}

// TestAwaitTagWakesOnTagAndBoundsMiss pins the AwaitTag contract: a tag that
// lands mid-grace wakes the waiter with true, and a run with no tag returns
// false after the grace instead of hanging.
func TestAwaitTagWakesOnTagAndBoundsMiss(t *testing.T) {
	newConnected := func() *DenialMonitor {
		return &DenialMonitor{done: make(chan struct{}), tagCh: make(chan struct{}), connected: true}
	}

	hit := newConnected()
	go func() {
		time.Sleep(5 * time.Millisecond)
		hit.markTagSeen()
	}()
	start := time.Now()
	if !hit.AwaitTag() {
		t.Fatal("AwaitTag must report a tag that lands during the grace")
	}
	if elapsed := time.Since(start); elapsed >= 15*time.Millisecond {
		t.Fatalf("AwaitTag hit took %v, want a wake before the grace timer", elapsed)
	}

	miss := newConnected()
	start = time.Now()
	if miss.AwaitTag() {
		t.Fatal("AwaitTag must report no tag when none lands")
	}
	if elapsed := time.Since(start); elapsed > 2*denialMonitorTagGrace {
		t.Fatalf("AwaitTag miss took %v, want a bounded grace wait", elapsed)
	}

	// A monitor that never connected, or one that already saw its tag, never
	// waits.
	noop := &DenialMonitor{done: make(chan struct{}), tagCh: make(chan struct{})}
	close(noop.done)
	if noop.AwaitTag() {
		t.Fatal("a disconnected monitor must never report a tag")
	}
	seen := newConnected()
	seen.markTagSeen()
	if !seen.AwaitTag() {
		t.Fatal("a monitor that already saw its tag must report it at once")
	}
}
