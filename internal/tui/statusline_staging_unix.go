//go:build !windows

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

// openStatuslineFileNoFollow opens path without following a final-component
// symlink, so a staged entry can never be a stand-in for a file elsewhere.
// The caller still Lstats first for a clear refusal message; O_NOFOLLOW is
// the TOCTOU-safe enforcement.
func openStatuslineFileNoFollow(path string, write bool) (*os.File, error) {
	flag := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	var perm uint32
	if write {
		flag = unix.O_WRONLY | unix.O_CREAT | unix.O_TRUNC | unix.O_NOFOLLOW | unix.O_CLOEXEC
		perm = 0o600
	}
	fd, err := unix.Open(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
