//go:build darwin

package tui

import "golang.org/x/sys/unix"

func init() {
	slAtomicSwap = func(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }
}
