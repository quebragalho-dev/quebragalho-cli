//go:build darwin

package tui

import (
	"sync"

	"github.com/ebitengine/purego"
)

// systemCommandHeld lê as teclas modificadoras da sessão local pelo
// CoreGraphics (CGEventSourceFlagsState). Não pede permissão de Monitoramento
// de Entrada. Falha ao carregar conta como não pressionado.
var systemCommandHeld = func() bool {
	cgOnce.Do(loadCGEventSourceFlags)
	if cgEventSourceFlagsState == nil {
		return false
	}
	return cgEventSourceFlagsState(cgEventSourceStateCombinedSessionState)&cgEventFlagMaskCommand != 0
}

const (
	cgEventSourceStateCombinedSessionState = 0        // kCGEventSourceStateCombinedSessionState
	cgEventFlagMaskCommand                 = 0x100000 // kCGEventFlagMaskCommand
)

var (
	cgOnce                  sync.Once
	cgEventSourceFlagsState func(stateID int32) uint64
)

func loadCGEventSourceFlags() {
	lib, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	addr, err := purego.Dlsym(lib, "CGEventSourceFlagsState")
	if err != nil {
		return
	}
	purego.RegisterFunc(&cgEventSourceFlagsState, addr)
}
