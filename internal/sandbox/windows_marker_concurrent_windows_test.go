//go:build windows

package sandbox

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Regression test for the CI failure in PR #106 (Windows run 44315337387):
// two MCP servers starting together failed with "initialize MCP server ok:
// EOF: qg-windows-command-runner.exe: read windows unelevated setup marker:
// ... is being used by another process" because one process replaced
// windows-unelevated-setup.json via rename while the other read it, and the
// open failed with ERROR_SHARING_VIOLATION. Hammer record+load from several
// goroutines; without the read retry the loads fail, with it every call
// succeeds.
//
// Lost plans are NOT asserted here: concurrent records still race on the
// read-modify-write sequence (no inter-process lock), so a save can drop a
// sibling's plan. That only costs a redundant ACL re-apply, never a failure.
func TestWindowsUnelevatedMarkerConcurrentReadDuringReplace(t *testing.T) {
	home := filepath.Join(t.TempDir(), "sandbox")
	seed := WindowsUnelevatedAppliedPlan{ACLPlanHash: "seed", ACLPlanEntries: 1}
	if err := recordWindowsUnelevatedAppliedPlan(home, seed); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	const writers = 3
	const readers = 3
	const iterations = 100

	start := make(chan struct{})
	errs := make(chan error, writers+readers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				// Unique fingerprints force a real read-write-rename every
				// call instead of the dedupe short-circuit.
				plan := WindowsUnelevatedAppliedPlan{
					ACLPlanHash:    fmt.Sprintf("writer-%d-plan-%d", w, i),
					ACLPlanEntries: i + 1,
				}
				if err := recordWindowsUnelevatedAppliedPlan(home, plan); err != nil {
					errs <- fmt.Errorf("writer %d iteration %d: record: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if _, err := loadWindowsUnelevatedSetupMarker(home); err != nil {
					errs <- fmt.Errorf("reader %d iteration %d: load: %w", r, i, err)
					return
				}
			}
		}(r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// Same race shape for the elevated setup marker: the setup helper replaces
// windows-setup.json via rename while concurrent runners validate it, and a
// mid-replace open fails with ERROR_SHARING_VIOLATION without the retry.
func TestWindowsSetupMarkerConcurrentValidateDuringReplace(t *testing.T) {
	config := WindowsSandboxSetupConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     `C:\workspace`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	if _, err := WriteWindowsSandboxSetupMarker(config); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	const writers = 2
	const readers = 4
	const iterations = 50

	start := make(chan struct{})
	errs := make(chan error, writers+readers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if _, err := WriteWindowsSandboxSetupMarker(config); err != nil {
					errs <- fmt.Errorf("writer %d iteration %d: write: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if err := ValidateWindowsSandboxSetupMarker(config); err != nil {
					errs <- fmt.Errorf("reader %d iteration %d: validate: %w", r, i, err)
					return
				}
			}
		}(r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
