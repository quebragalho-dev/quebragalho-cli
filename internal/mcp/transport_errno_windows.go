//go:build windows

package mcp

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// Errno half of the transport classifiers (reconnect.go) on Windows. A real
// Windows failure carries the raw Winsock error, and errors.Is against the
// POSIX syscall constant returns FALSE there because Go's Windows
// syscall.ECONNREFUSED is a distinct value the net package never produces —
// so the WSA codes are matched explicitly. The POSIX constants stay in the
// list too, so an error built with them (e.g. a test fixture) still
// classifies. Every predicate is an addition to the substring checks, never
// a replacement.

// isRefusedDialErrno reports whether err carries a refused-dial errno: the
// dial never left, so nothing reached the server.
func isRefusedDialErrno(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, windows.WSAECONNREFUSED)
}

// isResetErrno reports whether err carries a reset/aborted-connection errno:
// the peer dropped an established connection mid-stream.
func isResetErrno(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, windows.WSAECONNRESET) ||
		errors.Is(err, windows.WSAECONNABORTED)
}

// isClosedPipeErrno reports whether err carries a broken/closed-pipe errno:
// the stdio transport died.
func isClosedPipeErrno(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_NO_DATA)
}
