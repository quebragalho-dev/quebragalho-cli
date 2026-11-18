//go:build windows

package fsutil

import (
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// openFileShared opens path for reading with
// FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE, so a concurrent process
// can replace the file via rename while the read handle is open. A plain
// os.ReadFile open carries no delete share, and that rename then fails with
// ERROR_ACCESS_DENIED — the writer-side failure seen when two sandbox runners
// start together. Errors keep the *os.PathError shape with the raw Win32 code
// as syscall.Errno, so errors.Is(err, os.ErrNotExist) and the transient-lock
// classification behave exactly like os.ReadFile.
func openFileShared(path string) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	handle, err := windows.CreateFile(pathp, windows.GENERIC_READ, share, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errno, ok := err.(windows.Errno); ok {
			err = syscall.Errno(errno)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// readFileShared reads the whole file through a shared-access open.
func readFileShared(path string) ([]byte, error) {
	file, err := openFileShared(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, &os.PathError{Op: "read", Path: path, Err: err}
	}
	return data, nil
}
