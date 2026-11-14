package cli

import (
	"os"
	"strconv"
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
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = keyboard.Close()
		return nil, nil, errno
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		_ = keyboard.Close()
		return nil, nil, errno
	}
	tty, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(number), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = keyboard.Close()
		return nil, nil, err
	}
	return keyboard, tty, nil
}
