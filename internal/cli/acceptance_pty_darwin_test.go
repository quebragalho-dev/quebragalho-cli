package cli

import (
	"bytes"
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// openAcceptancePTY opens a pseudo-terminal pair: what the test writes to
// keyboard is what a program reading tty receives, and tty is a terminal.
func openAcceptancePTY() (keyboard *os.File, tty *os.File, err error) {
	keyboard, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := keyboard.Fd()
	for _, request := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, 0); errno != 0 {
			_ = keyboard.Close()
			return nil, nil, errno
		}
	}
	name := make([]byte, 128)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		_ = keyboard.Close()
		return nil, nil, errno
	}
	end := bytes.IndexByte(name, 0)
	if end <= 0 {
		_ = keyboard.Close()
		return nil, nil, errors.New("pseudo-terminal without a name")
	}
	tty, err = os.OpenFile(string(name[:end]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = keyboard.Close()
		return nil, nil, err
	}
	return keyboard, tty, nil
}
