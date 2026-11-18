//go:build (darwin || freebsd || linux || netbsd) && !android

package dictation

import (
	"fmt"
	"runtime"

	"github.com/ebitengine/purego"
)

type sherpaLibHandle uintptr

func sherpaCAPILibName() string {
	if runtime.GOOS == "darwin" {
		return "libsherpa-onnx-c-api.dylib"
	}
	return "libsherpa-onnx-c-api.so"
}

func openSherpaCAPILib(path string) (sherpaLibHandle, error) {
	h, err := purego.Dlopen(path, purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		return 0, fmt.Errorf("loading sherpa-onnx C API %q: %w", path, err)
	}
	return sherpaLibHandle(h), nil
}

func symSherpaCAPILib(h sherpaLibHandle, name string) (uintptr, error) {
	addr, err := purego.Dlsym(uintptr(h), name)
	if err != nil {
		return 0, fmt.Errorf("resolving %s: %w", name, err)
	}
	return addr, nil
}

func closeSherpaCAPILib(h sherpaLibHandle) {
	if h != 0 {
		_ = purego.Dlclose(uintptr(h))
	}
}
