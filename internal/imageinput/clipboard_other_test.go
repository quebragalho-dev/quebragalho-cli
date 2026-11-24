//go:build !darwin

package imageinput

import (
	"errors"
	"testing"
)

// Fora do macOS não existe leitura nativa: o stub diz "indisponível" e o
// despacho cai direto no caminho que já existia.
func TestReadClipboardContentNativeStubIsUnavailable(t *testing.T) {
	_, err := readClipboardContentNative(false)
	if !errors.Is(err, errNativeClipboardUnavailable) {
		t.Fatalf("err = %v, want errNativeClipboardUnavailable", err)
	}
}
