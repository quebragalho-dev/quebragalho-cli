//go:build !darwin && !windows

package dictation

func openNativeMic(sampleRate int) (micStream, error) {
	return nil, errNoNativeMic
}
