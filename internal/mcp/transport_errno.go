//go:build !windows && !plan9

package mcp

import (
	"errors"
	"syscall"
)

// Errno half of the transport classifiers (reconnect.go): a refused dial, a
// reset connection and a closed pipe are recognized by the errno the OS
// reports, not just by the English wording. The OS message is localized on
// Windows, so no substring can be trusted there; these predicates keep the
// classifiers working behind any wording. Every predicate is an addition to
// the substring checks, never a replacement.

// isRefusedDialErrno reports whether err carries a refused-dial errno: the
// dial never left, so nothing reached the server.
func isRefusedDialErrno(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// isResetErrno reports whether err carries a reset/aborted-connection errno:
// the peer dropped an established connection mid-stream.
func isResetErrno(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}

// isClosedPipeErrno reports whether err carries a broken/closed-pipe errno:
// the stdio transport died.
func isClosedPipeErrno(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
