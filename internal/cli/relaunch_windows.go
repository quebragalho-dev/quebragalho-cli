package cli

import (
	"os"
	"os/exec"
)

// relaunchBinary runs the updated binary with the same arguments and
// environment, waits for it, and reports its exit code. The .exe is locked
// while qg runs, so unlike Unix this cannot replace the live process: the
// caller exits with the reported code instead. stdio is inherited so the
// new session takes over the terminal seamlessly.
func relaunchBinary(newBinary string, args []string, env []string) (int, error) {
	command := exec.Command(newBinary, args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = env
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
