//go:build !windows

package fsutil

import "os"

// readFileShared matches the Windows shared-access open; elsewhere a plain
// read already tolerates a concurrent rename, so there is nothing to share.
func readFileShared(path string) ([]byte, error) {
	return os.ReadFile(path)
}
