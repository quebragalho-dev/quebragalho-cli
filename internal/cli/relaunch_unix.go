//go:build unix

package cli

import (
	"syscall"
)

// relaunchBinary replaces the running process with the updated binary,
// keeping the same arguments and environment. It only returns on failure.
func relaunchBinary(newBinary string, args []string, env []string) (int, error) {
	if err := syscall.Exec(newBinary, args, env); err != nil {
		return 1, err
	}
	return 0, nil
}
