package tools

import (
	"strings"
	"testing"
)

// Part 2 mandatory coverage: Windows sandbox startup hint.

func windowsPowerShellShell() shellRuntime {
	return shellRuntime{GOOS: "windows", Executable: "powershell.exe", Syntax: "PowerShell", Kind: shellKindPowerShell}
}

func windowsCmdShell() shellRuntime {
	return shellRuntime{GOOS: "windows", Executable: "cmd.exe", Syntax: "Windows cmd.exe", Kind: shellKindCmd}
}

func TestStartupHintPositives(t *testing.T) {
	pwshMarkers := "System.DllNotFoundException: Unable to load DLL 'BCrypt.dll' (0x8007045A)"
	clrOutput := "Falha na inicialização do CLR com HRESULT 80070005.\r\n"
	cases := []struct {
		name     string
		output   string
		exitCode int
		shell    shellRuntime
	}{
		{"powershell CLR powershell-shell", clrOutput, 4294901760, windowsPowerShellShell()},
		{"powershell CLR cmd-shell", clrOutput, 4294901760, windowsCmdShell()},
		{"pwsh bcrypt powershell-shell", pwshMarkers, 3762504530, windowsPowerShellShell()},
		{"native dll init cmd-shell", "", 3221225794, windowsCmdShell()},
		{"native dll init powershell-shell no-generalize", "some tool output", 3221225794, windowsPowerShellShell()},
		{"hresult alone", "something failed with HRESULT 80070005", 4294901760, windowsCmdShell()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := detectShellOutputIssueForRuntime(tc.output, tc.shell, tc.exitCode, true)
			if issue == nil {
				t.Fatalf("expected startup hint for %s", tc.name)
			}
			if issue.Kind != windowsSandboxStartupKind {
				t.Fatalf("kind = %q, want %q", issue.Kind, windowsSandboxStartupKind)
			}
			rendered := appendShellIssueHint("output:\n"+tc.output, *issue)
			if !strings.Contains(rendered, "[qg] shell issue:") {
				t.Fatalf("missing shell-issue prefix: %q", rendered)
			}
			if !strings.Contains(rendered, "Suggestion:") {
				t.Fatalf("missing Suggestion line: %q", rendered)
			}
			if !strings.Contains(issue.Suggestion, "require_escalated") {
				t.Fatalf("suggestion must mention require_escalated: %q", issue.Suggestion)
			}
			if strings.Contains(rendered, "qg sandbox setup") || strings.Contains(rendered, "qg doctor") {
				t.Fatalf("suggestion must not cite setup/doctor: %q", rendered)
			}
		})
	}
}

func TestStartupHintNegatives(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		exitCode int
		shell    shellRuntime
		wrapped  bool
	}{
		{"jail write denied is not startup", "Acesso negado", 1, windowsCmdShell(), true},
		{"dotnet crash without marker", "Unhandled exception. System.Exception: boom", 3762504530, windowsPowerShellShell(), true},
		{"not wrapped never hints", "Falha na inicialização do CLR com HRESULT 80070005.", 4294901760, windowsPowerShellShell(), false},
		{"linux never hints", "Falha na inicialização do CLR com HRESULT 80070005.", 4294901760, shellRuntime{GOOS: "linux", Executable: "/bin/sh", Syntax: "/bin/sh", Kind: shellKindPOSIX}, true},
		{"darwin never hints", "", 3221225794, shellRuntime{GOOS: "darwin", Executable: "/bin/sh", Syntax: "/bin/sh", Kind: shellKindPOSIX}, true},
		{"exit zero never hints", "Falha na inicialização do CLR com HRESULT 80070005.", 0, windowsPowerShellShell(), true},
		{"plain failure never hints", "something failed", 1, windowsCmdShell(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if issue := detectShellOutputIssueForRuntime(tc.output, tc.shell, tc.exitCode, tc.wrapped); issue != nil {
				t.Fatalf("expected no hint for %s, got %#v", tc.name, issue)
			}
		})
	}
}

func TestStartupHintHRESULTRequiresCLR(t *testing.T) {
	clrOutput := "Falha na inicialização do CLR com HRESULT 80070005."
	t.Run("clr hresult with exit 1 hints without shell sentence", func(t *testing.T) {
		issue := detectShellOutputIssueForRuntime(clrOutput, windowsPowerShellShell(), 1, true)
		if issue == nil {
			t.Fatalf("expected startup hint for CLR HRESULT with exit 1")
		}
		if issue.Kind != windowsSandboxStartupKind {
			t.Fatalf("kind = %q, want %q", issue.Kind, windowsSandboxStartupKind)
		}
		if strings.Contains(issue.Suggestion, "Other commands through this shell") {
			t.Fatalf("default HRESULT branch must not add shell sentence: %q", issue.Suggestion)
		}
		if !strings.Contains(issue.Suggestion, "require_escalated") {
			t.Fatalf("suggestion must mention require_escalated: %q", issue.Suggestion)
		}
	})
	t.Run("generic access denied hresult without clr never hints", func(t *testing.T) {
		output := "Access denied (HRESULT: 0x80070005)"
		if issue := detectShellOutputIssueForRuntime(output, windowsPowerShellShell(), 1, true); issue != nil {
			t.Fatalf("generic 80070005 without CLR must not hint, got %#v", issue)
		}
		if issue := detectShellOutputIssueForRuntime(output, windowsCmdShell(), 1, true); issue != nil {
			t.Fatalf("generic 80070005 without CLR must not hint, got %#v", issue)
		}
	})
	t.Run("clr without hresult never hints", func(t *testing.T) {
		for _, output := range []string{"error: clr.dll version mismatch", "CLR: unhandled exception in app"} {
			if issue := detectShellOutputIssueForRuntime(output, windowsPowerShellShell(), 1, true); issue != nil {
				t.Fatalf("CLR without 80070005 must not hint, got %#v", issue)
			}
			if issue := detectShellOutputIssueForRuntime(output, windowsCmdShell(), 1, true); issue != nil {
				t.Fatalf("CLR without 80070005 must not hint, got %#v", issue)
			}
		}
	})
}

func TestWrappedFromSandboxMeta(t *testing.T) {
	if !wrappedFromSandboxMeta(map[string]string{"sandbox_wrapped": "true"}) {
		t.Fatalf("expected true for sandbox_wrapped=true")
	}
	if wrappedFromSandboxMeta(map[string]string{"sandbox_wrapped": "false"}) {
		t.Fatalf("expected false for sandbox_wrapped=false")
	}
	if wrappedFromSandboxMeta(map[string]string{}) {
		t.Fatalf("expected false for missing key")
	}
	if wrappedFromSandboxMeta(map[string]string{"sandbox_wrapped": "yes"}) {
		t.Fatalf("expected false for invalid value")
	}
	if wrappedFromSandboxMeta(nil) {
		t.Fatalf("expected false for nil map")
	}
}

func TestStartupHintMsysPrecedence(t *testing.T) {
	msysOutput := `0 [main] head (3568) C:\Program Files\Git\usr\bin\head.exe: *** fatal error - CreateFileMapping S-1-5-21. Win32 error 5. Terminating.`
	shell := windowsCmdShell()
	issue := detectShellOutputIssueForRuntime(msysOutput, shell, 4294901760, true)
	if issue == nil {
		t.Fatalf("expected MSYS hint to win")
	}
	if issue.Kind != windowsMsysSandboxKind {
		t.Fatalf("precedence broken: kind = %q, want MSYS %q", issue.Kind, windowsMsysSandboxKind)
	}
}

func TestStartupHintAtMostOnePerResult(t *testing.T) {
	msysOutput := `0 [main] head (3568) C:\Program Files\Git\usr\bin\head.exe: *** fatal error - CreateFileMapping S-1-5-21. Win32 error 5. Terminating.`
	shell := windowsCmdShell()
	issue := detectShellOutputIssueForRuntime(msysOutput, shell, 4294901760, true)
	rendered := appendShellIssueHint("output:\n"+msysOutput, *issue)
	if count := strings.Count(rendered, "[qg] shell issue:"); count != 1 {
		t.Fatalf("expected exactly one hint, got %d in %q", count, rendered)
	}
}

func TestStartupHintPowerShellSentenceOnlyForOwnShell(t *testing.T) {
	clrOutput := "Falha na inicialização do CLR com HRESULT 80070005."
	pwshMarkers := "System.DllNotFoundException: Unable to load DLL 'BCrypt.dll' (0x8007045A)"

	powerShellCases := []struct {
		name     string
		output   string
		exitCode int
	}{
		{"powershell CLR", clrOutput, 4294901760},
		{"pwsh bcrypt", pwshMarkers, 3762504530},
	}
	for _, tc := range powerShellCases {
		t.Run(tc.name+" powershell-shell has sentence", func(t *testing.T) {
			issue := detectShellOutputIssueForRuntime(tc.output, windowsPowerShellShell(), tc.exitCode, true)
			if issue == nil {
				t.Fatalf("expected hint")
			}
			if !strings.Contains(issue.Suggestion, "Other commands through this shell") {
				t.Fatalf("powershell-shell signature must add shell sentence: %q", issue.Suggestion)
			}
		})
		t.Run(tc.name+" cmd-shell lacks sentence", func(t *testing.T) {
			issue := detectShellOutputIssueForRuntime(tc.output, windowsCmdShell(), tc.exitCode, true)
			if issue == nil {
				t.Fatalf("expected hint")
			}
			if strings.Contains(issue.Suggestion, "Other commands through this shell") {
				t.Fatalf("cmd-shell must not add shell sentence: %q", issue.Suggestion)
			}
		})
	}

	t.Run("c0000142 never generalizes even on powershell-shell", func(t *testing.T) {
		issue := detectShellOutputIssueForRuntime("", windowsPowerShellShell(), 3221225794, true)
		if issue == nil {
			t.Fatalf("expected hint")
		}
		if strings.Contains(issue.Suggestion, "Other commands through this shell") {
			t.Fatalf("0xC0000142 must never generalize: %q", issue.Suggestion)
		}
	})
}

func TestStartupHintWithoutSignatureIsByteIdenticalBash(t *testing.T) {
	meta := map[string]string{}
	before := formatBashOutput("hello\n", "", 1)
	after := formatBashOutputWithShellHintForRuntime("hello\n", "", 1, true, windowsCmdShell(), meta)
	if after != before {
		t.Fatalf("without signature bash must stay identical: before=%q after=%q", before, after)
	}
	if _, ok := meta["shell_issue"]; ok {
		t.Fatalf("without signature bash must not set shell_issue: %#v", meta)
	}
}

func TestStartupHintWithoutSignatureIsByteIdenticalExec(t *testing.T) {
	input := execToolResultInput{
		commandText: "echo hi",
		output:      "hello\n",
		exited:      true,
		exitCode:    1,
		wrapped:     true,
	}
	result := execToolResultWithBudgetForRuntime(input, true, windowsCmdShell())
	if !strings.Contains(result.Output, "hello") {
		t.Fatalf("exec without signature lost output: %q", result.Output)
	}
	if strings.Contains(result.Output, "[qg] shell issue:") {
		t.Fatalf("exec without signature must not hint: %q", result.Output)
	}
	if _, ok := result.Meta["shell_issue"]; ok {
		t.Fatalf("exec without signature must not set shell_issue: %#v", result.Meta)
	}
}

func TestStartupHintTextIsTrueAndShort(t *testing.T) {
	issue := detectShellOutputIssueForRuntime("Falha na inicialização do CLR com HRESULT 80070005.", windowsPowerShellShell(), 4294901760, true)
	if issue == nil {
		t.Fatalf("expected hint")
	}
	text := issue.Message + "\n" + issue.Suggestion
	if len(text) > 800 {
		t.Fatalf("hint text too long (%d): %q", len(text), text)
	}
	for _, want := range []string{"could not start", "sandbox", "restricted-token", "PowerShell", "require_escalated"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hint text missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "qg sandbox setup") || strings.Contains(text, "qg doctor") {
		t.Fatalf("hint must not cite setup/doctor: %q", text)
	}
}
