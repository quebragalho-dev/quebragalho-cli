//go:build windows

package sandbox

import (
	"golang.org/x/sys/windows"
)

// windowsLogicalDriveRoots lists every mounted drive root ("C:\", ...). An
// error or an empty mask (no drives visible to the caller) yields nil so the
// caller falls back to the current-drive root.
func windowsLogicalDriveRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	return windowsDriveRootsFromBitmask(mask)
}
