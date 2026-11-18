//go:build windows

package sandbox

import (
	"fmt"
	"io"

	"golang.org/x/sys/windows"
)

func runWindowsSandboxSetup(config WindowsSandboxSetupConfig, stderr io.Writer) int {
	// Applying the WFP network filters and workspace ACLs requires Administrator
	// rights; without them WFP fails deep inside with a raw ACCESS_DENIED (0x5).
	// Check up front and return an actionable message instead.
	if !windowsProcessIsElevated() {
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": Administrator rights are required. Re-run `qg sandbox setup` from an elevated (Run as administrator) terminal.")
		return 1
	}
	plan, err := BuildWindowsACLPlan(config.commandConfig())
	if err != nil {
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 1
	}
	// Always provision the mode-INDEPENDENT infrastructure: the outbound block
	// filters scoped to the offline-marker SID. Runtime gates network per command
	// by whether the token carries that SID, so one setup serves both modes.
	networkPlan, err := BuildWindowsNetworkInfraPlan(config.commandConfig())
	if err != nil {
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 1
	}
	rollback, err := applyWindowsACLPlan(plan)
	if err != nil {
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 1
	}
	if err := applyWindowsNetworkPlan(networkPlan); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			fmt.Fprintf(stderr, "%s: %v; rollback failed: %v\n", WindowsSandboxSetupName, err, rollbackErr)
			return 1
		}
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 1
	}
	if _, err := WriteWindowsSandboxSetupMarker(config); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			fmt.Fprintf(stderr, "%s: %v; rollback failed: %v\n", WindowsSandboxSetupName, err, rollbackErr)
			return 1
		}
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 1
	}
	return 0
}

// windowsProcessIsElevated reports whether the current process runs with an
// elevated (Administrator) token. On any error obtaining the token it returns
// true so the setup proceeds and surfaces the real WFP/ACL error rather than a
// false "needs admin" claim.
func windowsProcessIsElevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return true
	}
	defer token.Close()
	return token.IsElevated()
}

// ensureWindowsSandboxSetup validates the setup marker and heals a
// plan-membership miss by applying the current workspace plan on demand, so
// one `qg sandbox setup` serves every folder instead of bricking all but one
// with "out of date". DACL edits on user-owned workspace and temp roots need
// no Administrator rights; only the machine-global WFP network filters do, and
// those stay gated behind the elevated setup (a network-infrastructure
// mismatch still fails with the setup hint). The apply rollback is deliberately
// discarded: the grants name synthetic capability SIDs no other token carries.
func ensureWindowsSandboxSetup(config WindowsSandboxCommandConfig) error {
	setup := WindowsSandboxSetupConfigFromCommand(config)
	if err := ValidateWindowsSandboxSetupMarker(setup); err == nil {
		return nil
	} else if !IsWindowsSandboxSetupPlanMismatch(err) {
		return err
	}
	applied, plan, err := buildWindowsSandboxSetupAppliedPlan(setup.commandConfig())
	if err != nil {
		return err
	}
	if _, err := applyWindowsACLPlan(plan); err != nil {
		return fmt.Errorf("apply windows sandbox workspace ACLs: %w — run `qg sandbox setup` from an elevated (Administrator) terminal, or re-run with `--sandbox forbid` to skip OS sandboxing", err)
	}
	return recordWindowsSandboxSetupAppliedPlan(setup.SandboxHome, applied)
}
