package daemon

import (
	"testing"
)

// A second finish must be a no-op, not a "close of closed channel" panic:
// timeouts, Drain-kill and retries must be able to race the normal path.
func TestSessionFinishIdempotent(t *testing.T) {
	sess := newSession("s", "", 0)
	sess.Started()
	sess.finish(0, nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second finish panicked: %v (want idempotent no-op)", r)
		}
	}()
	sess.finish(0, nil)

	select {
	case <-sess.Done():
	default:
		t.Fatal("Done must stay closed after a second finish")
	}
	if sess.State() != SessionDone {
		t.Fatalf("state = %s, want done after idempotent finish", sess.State())
	}
}
