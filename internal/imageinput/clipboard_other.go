//go:build !darwin

package imageinput

// readClipboardContentNative não existe fora do macOS: o despacho de
// readClipboardContentDarwin só o usa no darwin, e aqui ele responde sempre
// "indisponível" para o caminho de reserva ser o único.
func readClipboardContentNative(bool) (ClipboardContent, error) {
	return ClipboardContent{}, errNativeClipboardUnavailable
}
