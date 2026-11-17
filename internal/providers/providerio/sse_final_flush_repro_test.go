package providerio

import (
	"bufio"
	"strings"
	"testing"
)

// Repro for M2: the final flush after the scanner loop must respect handle's
// false (provider asked to stop because it already emitted its terminal
// error). On dev it ignores the return and reports a second generic
// "sse stream ended without [DONE]" error on top of the classified one.
func TestScanFinalFlushRespectsHandleFalse(t *testing.T) {
	body := "data: bad\n" // no blank line, no [DONE]: final-flush path
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 4096), maxSSELineBytes)
	calls := 0
	err := scanSSEPayloads(scanner, func(data string) bool {
		calls++
		if data != "bad" {
			t.Fatalf("payload = %q, want %q", data, "bad")
		}
		return false
	}, nil)
	if calls != 1 {
		t.Fatalf("handle calls = %d, want 1", calls)
	}
	if err != nil {
		t.Fatalf("final flush must respect handle=false and end clean, got %v", err)
	}
}
