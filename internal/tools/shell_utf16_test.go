package tools

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// Part 1 mandatory coverage for Windows UTF-16LE normalization.

func encodeUTF16LEForTest(s string) string {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return string(buf)
}

func TestNormalizeWindowsProcessOutputMeasuredBytes(t *testing.T) {
	raw := encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	got := normalizeWindowsProcessOutputForGOOS(raw, "windows")
	want := "Falha na inicialização do CLR com HRESULT 80070005.\r\n"
	if got != want {
		t.Fatalf("measured conversion = %q, want %q", got, want)
	}
	if !strings.Contains(got, "ç") || !strings.Contains(got, "ã") {
		t.Fatalf("measured conversion lost accented bytes: %q", got)
	}
}

func TestNormalizeWindowsProcessOutputStripsBOM(t *testing.T) {
	raw := "\xff\xfe" + encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	got := normalizeWindowsProcessOutputForGOOS(raw, "windows")
	if strings.Contains(got, "\x00") || strings.Contains(got, "\xff") || strings.Contains(got, "\xfe") {
		t.Fatalf("BOM output still carries raw bytes: %q", got)
	}
	if !strings.Contains(got, "HRESULT 80070005") {
		t.Fatalf("BOM conversion lost marker: %q", got)
	}
}

func TestNormalizeWindowsProcessOutputMixedPreservesUTF8Prefix(t *testing.T) {
	raw := "hi\r\n" + encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	got := normalizeWindowsProcessOutputForGOOS(raw, "windows")
	if !strings.HasPrefix(got, "hi\r\n") {
		t.Fatalf("mixed conversion lost UTF-8 prefix: %q", got)
	}
	if !strings.Contains(got, "HRESULT 80070005") {
		t.Fatalf("mixed conversion lost UTF-16 section: %q", got)
	}
	if strings.Contains(got, "\x00") {
		t.Fatalf("mixed conversion left NULs: %q", got)
	}
}

func TestNormalizeWindowsProcessOutputLeavesValidUTF8Alone(t *testing.T) {
	for _, input := range []string{
		"build succeeded\n",
		"ok  \tpackage example\n",
		"Falha na inicialização já em UTF-8 com ç ã\n",
	} {
		if got := normalizeWindowsProcessOutputForGOOS(input, "windows"); got != input {
			t.Fatalf("valid UTF-8 altered: %q -> %q", input, got)
		}
		if got := normalizeWindowsProcessOutputForGOOS(input, "linux"); got != input {
			t.Fatalf("linux identity broken: %q -> %q", input, got)
		}
		if got := normalizeWindowsProcessOutputForGOOS(input, "darwin"); got != input {
			t.Fatalf("darwin identity broken: %q -> %q", input, got)
		}
	}
}

func TestNormalizeWindowsProcessOutputLeavesIsolatedNULAlone(t *testing.T) {
	inputs := []string{
		"a\x00b",
		"prefix\x00suffix\n",
		"\x01\x00\x02",
		"\x00",
		"ab\x00",
	}
	for _, input := range inputs {
		if got := normalizeWindowsProcessOutputForGOOS(input, "windows"); got != input {
			t.Fatalf("isolated NUL input altered on windows: %q -> %q", input, got)
		}
		if got := normalizeWindowsProcessOutputForGOOS(input, "linux"); got != input {
			t.Fatalf("isolated NUL input altered on linux: %q -> %q", input, got)
		}
	}
}

func TestNormalizeWindowsProcessOutputLeavesShortBinaryAlone(t *testing.T) {
	raw := "\x01\x00\x02\x00\x03"
	if got := normalizeWindowsProcessOutputForGOOS(raw, "windows"); got != raw {
		t.Fatalf("short binary altered: %q -> %q", raw, got)
	}
}

func TestNormalizeWindowsProcessOutputPreservesOddTrailingByte(t *testing.T) {
	full := encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	// Simulate a truncated capture ending mid-pair: drop the final high byte.
	truncated := full[:len(full)-1]
	got := normalizeWindowsProcessOutputForGOOS(truncated, "windows")
	if !strings.Contains(got, "HRESULT 80070005") {
		t.Fatalf("odd-trailing conversion lost readable prefix: %q", got)
	}
	if !strings.HasSuffix(got, "\x05") && !strings.Contains(got, "80070005") {
		t.Fatalf("odd trailing byte not preserved readably: %q", got)
	}
	// Must remain valid UTF-8 and must not panic or drop the prefix.
	if len(got) == 0 {
		t.Fatalf("odd-trailing conversion returned empty")
	}
}

func TestNormalizeWindowsProcessOutputSplitPairAcrossPolls(t *testing.T) {
	full := encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	if len(full) < 10 {
		t.Fatalf("fixture too short: %d", len(full))
	}
	// Cut in the middle of a UTF-16 code unit (odd split).
	split := 21
	first := full[:split]
	second := full[split:]
	convertedFirst := normalizeWindowsProcessOutputForGOOS(first, "windows")
	convertedSecond := normalizeWindowsProcessOutputForGOOS(second, "windows")
	// Minimum bar: no corruption, valid UTF-8, prefix/suffix survive across the split.
	if !strings.Contains(convertedFirst, "Falha") {
		t.Fatalf("first chunk lost prefix: %q", convertedFirst)
	}
	if !strings.Contains(convertedSecond, "HRESULT 80070005") {
		t.Fatalf("second chunk lost suffix: %q", convertedSecond)
	}
	combined := convertedFirst + convertedSecond
	if len(combined) == 0 {
		t.Fatalf("split conversion returned empty")
	}
	// Whole-input conversion is the ideal; chunked conversion must not corrupt.
	whole := normalizeWindowsProcessOutputForGOOS(full, "windows")
	if !strings.Contains(whole, "Falha") || !strings.Contains(whole, "HRESULT 80070005") {
		t.Fatalf("whole conversion broken: %q", whole)
	}
}

func TestNormalizeWindowsProcessOutputLinuxIsIdentityEvenForUTF16(t *testing.T) {
	raw := encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	if got := normalizeWindowsProcessOutputForGOOS(raw, "linux"); got != raw {
		t.Fatalf("linux must stay byte-identical: %q", got)
	}
	if got := normalizeWindowsProcessOutputForGOOS(raw, "darwin"); got != raw {
		t.Fatalf("darwin must stay byte-identical: %q", got)
	}
}

func TestPrepareBashOutputConvertsBeforeBudget(t *testing.T) {
	raw := encodeUTF16LEForTest("Falha na inicialização do CLR com HRESULT 80070005.\r\n")
	out, errText, _ := prepareBashOutputForGOOS(raw, len(raw), "", 0, map[string]string{}, true, "windows")
	if strings.Contains(out+errText, "\x00") {
		t.Fatalf("budgeted bash output still carries NULs: %q %q", out, errText)
	}
	if !strings.Contains(out, "HRESULT 80070005") {
		t.Fatalf("budgeted bash output lost marker: %q", out)
	}
}

func TestFormatBashOutputConvertsBeforeRedaction(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	raw := encodeUTF16LEForTest("key " + secret + " end")
	out := formatBashOutputForGOOS(raw, "", 0, "windows")
	if strings.Contains(out, secret) {
		t.Fatalf("bash secret leaked after conversion: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:aws_access_key_id]") {
		t.Fatalf("bash secret not redacted after conversion: %q", out)
	}
}

func TestExecToolResultConvertsBeforeTruncationAndRedaction(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	raw := encodeUTF16LEForTest("key " + secret + " done " + strings.Repeat("x", 100))
	result := execToolResultWithBudgetForGOOS(execToolResultInput{
		commandText:     "powershell.exe",
		output:          raw,
		exited:          true,
		exitCode:        1,
		maxOutputTokens: 10000,
	}, true, "windows")
	if strings.Contains(result.Output, "\x00") {
		t.Fatalf("exec output still carries NULs: %q", result.Output)
	}
	scrubbed := scrubResultSecrets(result)
	if strings.Contains(scrubbed.Output, secret) {
		t.Fatalf("exec secret leaked through registry: %q", scrubbed.Output)
	}
}

func TestWslServiceDeniedStillMatchesAfterConversion(t *testing.T) {
	var utf16ish strings.Builder
	for _, r := range "Access is denied.\r\nError code: Bash/Service/CreateInstance/E_ACCESSDENIED\r\n" {
		utf16ish.WriteRune(r)
		utf16ish.WriteByte(0)
	}
	converted := normalizeWindowsProcessOutputForGOOS(utf16ish.String(), "windows")
	shell := shellRuntime{GOOS: "windows", Executable: "cmd.exe", Syntax: "Windows cmd.exe", Kind: shellKindCmd}
	if issue := detectShellOutputIssueForRuntime(converted, shell); issue == nil || issue.Kind != windowsMsysSandboxKind {
		t.Fatalf("converted WSL output lost MSYS hint: %#v", issue)
	}
	// Unconverted input must keep working too (existing tests cover it, pinned here).
	if issue := detectShellOutputIssueForRuntime(utf16ish.String(), shell); issue == nil || issue.Kind != windowsMsysSandboxKind {
		t.Fatalf("raw UTF-16ish WSL output lost MSYS hint: %#v", issue)
	}
}
