//go:build windows

package dictation

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type sherpaLibHandle windows.Handle

func sherpaCAPILibName() string {
	return "sherpa-onnx-c-api.dll"
}

func openSherpaCAPILib(path string) (sherpaLibHandle, error) {
	// LOAD_WITH_ALTERED_SEARCH_PATH lets Windows resolve sibling onnxruntime.dll
	// from the directory containing sherpa-onnx-c-api.dll.
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
	if err != nil {
		return 0, fmt.Errorf("loading sherpa-onnx C API %q: %w", path, err)
	}
	return sherpaLibHandle(h), nil
}

func symSherpaCAPILib(h sherpaLibHandle, name string) (uintptr, error) {
	addr, err := windows.GetProcAddress(windows.Handle(h), name)
	if err != nil {
		return 0, fmt.Errorf("resolving %s: %w", name, err)
	}
	return addr, nil
}
