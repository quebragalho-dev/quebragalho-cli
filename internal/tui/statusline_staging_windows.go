//go:build windows

package tui

import (
	"os"
)

// openStatuslineFileNoFollow opens a staging file. Windows has no O_NOFOLLOW
// equivalent for plain files, so callers gate with Lstat first and treat the
// open as best-effort containment there.
func openStatuslineFileNoFollow(path string, write bool) (*os.File, error) {
	if write {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	}
	return os.Open(path)
}
