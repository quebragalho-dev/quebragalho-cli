//go:build !windows

package tui

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isSymlinkRefusal reports an open failure caused by O_NOFOLLOW meeting a
// symlink: the kernel compares errnos, so this matches ELOOP (and EMLINK)
// instead of comparing message text.
func isSymlinkRefusal(err error) bool {
	return err != nil && (errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK))
}
