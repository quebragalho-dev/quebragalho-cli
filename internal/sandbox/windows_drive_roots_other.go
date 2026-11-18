//go:build !windows

package sandbox

// windowsLogicalDriveRoots has no meaning off Windows; the policy falls back
// to the current-drive root.
func windowsLogicalDriveRoots() []string {
	return nil
}
