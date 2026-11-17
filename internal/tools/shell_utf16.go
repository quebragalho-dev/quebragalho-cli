package tools

import (
	"strings"
	"unicode/utf8"
)

// Windows process output arrives as UTF-16LE when a console-mode program
// writes to a piped stderr under the restricted token (powershell 5.1 CLR
// failure, pwsh DllNotFoundException, ...). The byte capture keeps the raw
// bytes, which render as ASCII interleaved with NULs and escape secret
// redaction. This file decodes UTF-16LE runs back to UTF-8 before redaction
// and before output budgeting, only on Windows.
//
// Design notes:
//   - Pure and testable on any OS via the explicit goos parameter; production
//     callers pass runtimeGOOS()/shell.GOOS, Linux and macOS stay identical.
//   - Segment-based: scans for runs of at least minUTF16Units consecutive
//     UTF-16LE code units with a zero high byte and a plausible low byte,
//     decodes those runs, copies everything else byte-for-byte. This keeps
//     valid UTF-8 (no NUL) identical, leaves isolated NULs and short binaries
//     untouched, and converts the UTF-16 tail of mixed buffers ("hi\r\n" in
//     UTF-8 followed by UTF-16) while preserving the prefix.
//   - A trailing odd byte (split UTF-16 pair across polls) is preserved as-is:
//     no corruption, at most 1-2 raw bytes remain when a pair is cut between
//     two polls. Full-output paths (bash capture, exec final result) see the
//     concatenated bytes and convert entirely.

const minUTF16Units = 4

// normalizeWindowsProcessOutputForGOOS converts captured process output from
// UTF-16LE to UTF-8 when running on Windows. On any other GOOS it returns the
// input unchanged.
func normalizeWindowsProcessOutputForGOOS(output string, goos string) string {
	if goos != "windows" {
		return output
	}
	return decodeUTF16LESegments(output)
}

func isPlausibleUTF16Low(b byte) bool {
	if b == 0x09 || b == 0x0A || b == 0x0D {
		return true
	}
	if b >= 0x20 && b <= 0x7E {
		return true
	}
	// Latin-1 supplement (includes 0xE7/0xE3 of the measured CLR message).
	if b >= 0xA0 {
		return true
	}
	return false
}

func decodeUTF16LESegments(s string) string {
	raw := []byte(s)
	if len(raw) == 0 {
		return s
	}
	hasNUL := false
	for _, b := range raw {
		if b == 0 {
			hasNUL = true
			break
		}
	}
	if !hasNUL {
		return s
	}
	var out strings.Builder
	out.Grow(len(raw))
	i := 0
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		i = 2
	}
	for i < len(raw) {
		j := i
		for j+1 < len(raw) && raw[j+1] == 0 && isPlausibleUTF16Low(raw[j]) {
			j += 2
		}
		units := (j - i) / 2
		if units >= minUTF16Units {
			for k := i; k < j; k += 2 {
				r := rune(raw[k])
				if r < utf8.RuneSelf {
					out.WriteByte(byte(r))
				} else {
					out.WriteRune(r)
				}
			}
			i = j
			continue
		}
		out.WriteByte(raw[i])
		i++
	}
	return out.String()
}

// normalizeBashStreamForGOOS converts one captured stream and adjusts its
// total for budgeting. When nothing changes the total is preserved (keeps the
// capture-gap accounting); when conversion shrinks a complete stream the
// total becomes the converted length so the budget counts model-visible bytes.
// On overflow (total exceeds the retained string) the original total is kept,
// staying conservative rather than guessing the dropped middle's decoded size.
func normalizeBashStreamForGOOS(stream string, total int, goos string) (string, int) {
	converted := normalizeWindowsProcessOutputForGOOS(stream, goos)
	if converted == stream {
		return stream, total
	}
	if total <= len(stream) {
		return converted, len(converted)
	}
	return converted, total
}
