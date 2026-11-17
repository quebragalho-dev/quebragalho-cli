package daemon

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestClientHandshakeTimesOut verifies the handshake is bounded: a peer that
// accepts the connection but never completes the version exchange makes
// NewClientConn fail promptly instead of blocking forever (D9).
func TestClientHandshakeTimesOut(t *testing.T) {
	orig := handshakeTimeout
	handshakeTimeout = 100 * time.Millisecond
	defer func() { handshakeTimeout = orig }()

	// net.Pipe is synchronous: with no reader on the server end the client's hello
	// write blocks, so only the handshake deadline can unblock it.
	client, server := net.Pipe()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		_, err := NewClientConn(client)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("NewClientConn must fail when the peer never completes the handshake")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NewClientConn blocked past the handshake timeout — deadline not applied")
	}
}

// serveClientStream runs the fake peer side of a Run/Attach stream on server:
// it completes the handshake, consumes the single Run/Attach request, then
// replays one data line followed by an end frame carrying endMessage (empty
// for a normal end). It reports the first peer-side failure on done.
func serveClientStream(server net.Conn, endMessage string, done chan<- error) {
	hello, err := ReadControl(server)
	if err != nil {
		done <- err
		return
	}
	if hello.Type != CtrlHello {
		done <- &net.OpError{Op: "handshake"}
		return
	}
	if err := WriteControl(server, Ctrl{Type: CtrlHelloOK, Version: ProtoVersion}); err != nil {
		done <- err
		return
	}
	if _, err := ReadControl(server); err != nil {
		done <- err
		return
	}
	if err := WriteControl(server, Ctrl{Type: CtrlData, Line: `{"type":"event"}`}); err != nil {
		done <- err
		return
	}
	if err := WriteControl(server, Ctrl{Type: CtrlEnd, Message: endMessage}); err != nil {
		done <- err
		return
	}
	done <- nil
}

// TestClientStreamEndWithMessageIsAnError verifies a truncated stream is not
// reported as success: an end frame carrying a message (the daemon shutting
// down mid-run) must surface as an error for both Run and Attach, while the
// data delivered before the cut is still passed to onLine.
func TestClientStreamEndWithMessageIsAnError(t *testing.T) {
	for _, method := range []string{"run", "attach"} {
		t.Run(method, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			serverDone := make(chan error, 1)
			go serveClientStream(serverConn, "daemon shutting down", serverDone)

			client, err := NewClientConn(clientConn)
			if err != nil {
				t.Fatalf("NewClientConn: %v", err)
			}
			var got []string
			var streamErr error
			if method == "run" {
				streamErr = client.Run("s", "", "hi", nil, func(line string) { got = append(got, line) })
			} else {
				streamErr = client.Attach("s", func(line string) { got = append(got, line) })
			}
			_ = client.Close()
			if err := <-serverDone; err != nil {
				t.Fatalf("fake peer failed: %v", err)
			}
			if len(got) != 1 || got[0] != `{"type":"event"}` {
				t.Fatalf("delivered lines = %#v, want the single data line before the cut", got)
			}
			if streamErr == nil || !strings.Contains(streamErr.Error(), "daemon shutting down") {
				t.Fatalf("stream error = %v, want it to mention the shutdown", streamErr)
			}
		})
	}
}

// TestClientStreamPlainEndIsSuccess verifies a normal end frame with no
// message still reports success.
func TestClientStreamPlainEndIsSuccess(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	serverDone := make(chan error, 1)
	go serveClientStream(serverConn, "", serverDone)

	client, err := NewClientConn(clientConn)
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}
	if err := client.Run("s", "", "hi", nil, func(string) {}); err != nil {
		_ = client.Close()
		t.Fatalf("Run on a plain end = %v, want nil", err)
	}
	_ = client.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("fake peer failed: %v", err)
	}
}
