package tools

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// Reproduction for win-startup-hint-forja: Windows process output arrives as
// UTF-16LE and startup failures inside the restricted sandbox get no hint.
// These tests assert the desired behavior through the real bash and
// exec_command formatting paths; they fail on dev intocado.

// encodeUTF16LE renders s as UTF-16LE bytes kept in a Go string (one byte per
// char, NUL high bytes for the BMP range used here).
func encodeUTF16LE(s string) string {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return string(buf)
}

func measuredCLRText() string {
	return "Falha na inicialização do CLR com HRESULT 80070005.\r\n"
}

func measuredCLRUTF16() string {
	return encodeUTF16LE(measuredCLRText())
}

func TestReproBashMeasuredUTF16IsReadable(t *testing.T) {
	out := formatBashOutputForGOOS(measuredCLRUTF16(), "", 4294901760, "windows")
	if strings.Contains(out, "\x00") {
		t.Fatalf("bash output still carries NUL bytes: %q", out)
	}
	for _, want := range []string{"HRESULT 80070005", "Falha"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bash output missing %q: %q", want, out)
		}
	}
}

func TestReproBashUTF16WithBOMIsReadable(t *testing.T) {
	raw := "\xff\xfe" + measuredCLRUTF16()
	out := formatBashOutputForGOOS(raw, "", 4294901760, "windows")
	if strings.Contains(out, "\x00") {
		t.Fatalf("bash BOM output still carries NUL bytes: %q", out)
	}
	if !strings.Contains(out, "HRESULT 80070005") {
		t.Fatalf("bash BOM output missing HRESULT marker: %q", out)
	}
}

func TestReproBashMixedUTF8AndUTF16PreservesBoth(t *testing.T) {
	raw := "hi\r\n" + measuredCLRUTF16()
	out := formatBashOutputForGOOS(raw, "", 4294901760, "windows")
	if !strings.Contains(out, "hi") {
		t.Fatalf("bash mixed output lost UTF-8 prefix: %q", out)
	}
	if !strings.Contains(out, "HRESULT 80070005") {
		t.Fatalf("bash mixed output lost UTF-16 section: %q", out)
	}
	if strings.Contains(out, "\x00") {
		t.Fatalf("bash mixed output still carries NUL bytes: %q", out)
	}
}

func TestReproBashUTF16SecretIsRedacted(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	raw := encodeUTF16LE("leaked key " + secret + " done")
	out := formatBashOutputForGOOS(raw, "", 4294901760, "windows")
	if strings.Contains(out, secret) {
		t.Fatalf("bash output leaked UTF-16 secret: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:aws_access_key_id]") {
		t.Fatalf("bash output missing redaction placeholder: %q", out)
	}
}

func TestReproExecMeasuredUTF16IsReadable(t *testing.T) {
	result := execToolResultWithBudgetForGOOS(execToolResultInput{
		commandText: "powershell.exe",
		output:      measuredCLRUTF16(),
		exited:      true,
		exitCode:    4294901760,
	}, true, "windows")
	if strings.Contains(result.Output, "\x00") {
		t.Fatalf("exec output still carries NUL bytes: %q", result.Output)
	}
	if !strings.Contains(result.Output, "HRESULT 80070005") {
		t.Fatalf("exec output missing HRESULT marker: %q", result.Output)
	}
}

func TestReproExecUTF16SecretIsRedactedThroughRegistry(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	result := execToolResultWithBudgetForGOOS(execToolResultInput{
		commandText: "powershell.exe",
		output:      encodeUTF16LE("leaked key " + secret + " done"),
		exited:      true,
		exitCode:    4294901760,
	}, true, "windows")
	scrubbed := scrubResultSecrets(result)
	if !strings.Contains(scrubbed.Output, "[REDACTED]") {
		t.Fatalf("exec output missing registry redaction placeholder: %q", scrubbed.Output)
	}
	if strings.Contains(scrubbed.Output, secret) {
		t.Fatalf("exec output leaked UTF-16 secret through registry: %q", scrubbed.Output)
	}
}

func TestReproBashStartupHintForMeasuredFailures(t *testing.T) {
	pwshMarkers := encodeUTF16LE("System.DllNotFoundException: Unable to load DLL 'BCrypt.dll' (0x8007045A)")
	cases := []struct {
		name     string
		output   string
		exitCode int
	}{
		{"powershell CLR", measuredCLRUTF16(), 4294901760},
		{"pwsh bcrypt", pwshMarkers, 3762504530},
		{"native dll init", "", 3221225794},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]string{}
			out := formatBashOutputWithShellHintForGOOS("stdout-stub", tc.output, tc.exitCode, true, meta, "windows")
			if !strings.Contains(out, "[qg] shell issue:") || !strings.Contains(out, "require_escalated") {
				t.Fatalf("missing startup hint for %s (exit %d): %q meta=%#v", tc.name, tc.exitCode, out, meta)
			}
		})
	}
}

func TestReproExecStartupHintForMeasuredFailures(t *testing.T) {
	pwshMarkers := encodeUTF16LE("System.DllNotFoundException: Unable to load DLL 'BCrypt.dll' (0x8007045A)")
	cases := []struct {
		name     string
		output   string
		exitCode int
	}{
		{"powershell CLR", measuredCLRUTF16(), 4294901760},
		{"pwsh bcrypt", pwshMarkers, 3762504530},
		{"native dll init", "", 3221225794},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := execToolResultWithBudgetForGOOS(execToolResultInput{
				commandText: "powershell.exe",
				output:      tc.output,
				exited:      true,
				exitCode:    tc.exitCode,
				wrapped:     true,
			}, true, "windows")
			if !strings.Contains(result.Output, "[qg] shell issue:") || !strings.Contains(result.Output, "require_escalated") {
				t.Fatalf("missing exec startup hint for %s (exit %d): %q", tc.name, tc.exitCode, result.Output)
			}
		})
	}
}
