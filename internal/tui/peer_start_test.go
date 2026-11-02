package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

// TestStartPeerServiceReturnsWithBlockingForward pins the startup contract:
// a failed peer start must not hang the goroutine about to call program.Run,
// even when forward blocks the way program.Send does before the event loop
// starts reading. Without the `go` on the error send this test deadlocks
// until the timeout fires.
func TestStartPeerServiceReturnsWithBlockingForward(t *testing.T) {
	service, err := peermsg.New(peermsg.Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// A closed service fails Start immediately without touching the network.
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	forward := func(msg tea.Msg) {
		if _, ok := msg.(peerRuntimeErrorMsg); ok {
			<-release
		}
	}
	done := make(chan bool, 1)
	go func() {
		done <- startPeerService(service, forward)
	}()
	select {
	case started := <-done:
		if started {
			t.Fatal("startPeerService = true, want false for a failed start")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startPeerService blocked on a blocking forward; TUI startup would hang")
	}
	close(release)
}
