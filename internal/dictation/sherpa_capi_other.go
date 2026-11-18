//go:build !((darwin || freebsd || linux || netbsd) && !android) && !windows

package dictation

import "errors"

type sherpaLibHandle uintptr

func sherpaCAPILibName() string {
	return ""
}

func openSherpaCAPILib(string) (sherpaLibHandle, error) {
	return 0, errors.New("sherpa-onnx C API is not supported on this platform")
}

func symSherpaCAPILib(sherpaLibHandle, string) (uintptr, error) {
	return 0, errors.New("sherpa-onnx C API is not supported on this platform")
}

func closeSherpaCAPILib(sherpaLibHandle) {}
