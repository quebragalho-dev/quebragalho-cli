//go:build !windows

package tui

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// statuslineTestProcessAlive reports whether pid still runs (zombies count
// as gone: the group kill reaps through init).
func statuslineTestProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return !statuslineTestProcessZombie(pid)
}

// statuslineTestProcessZombie reports a zombie state via ps, mirroring the
// execution package's reap-aware check without importing test-only helpers.
func statuslineTestProcessZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
