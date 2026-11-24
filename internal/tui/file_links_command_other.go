//go:build !darwin

package tui

// systemCommandHeld fora do macOS não há como ler o Command: conta como não
// pressionado, e Ctrl/Alt continuam sendo os modificadores de link.
var systemCommandHeld = func() bool { return false }
