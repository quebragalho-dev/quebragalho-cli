//go:build windows

package tui

// isSymlinkRefusal reports an open failure caused by O_NOFOLLOW meeting a
// symlink. Windows has no O_NOFOLLOW, so the open never refuses this way:
// containment there rests on the Lstat gate in openStagingFileChecked, and
// this is always false.
func isSymlinkRefusal(err error) bool {
	return false
}
