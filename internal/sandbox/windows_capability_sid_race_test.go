package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent spawns through the sandboxed runner hit
// LoadOrCreateWindowsCapabilitySIDs on a fresh home at the same time: every
// caller reads "no file", mints its own SIDs, and the last save wins, so the
// processes disagree about the SIDs. Every caller must observe the same
// ReadOnly and Offline SIDs as the file on disk at the end.
func TestWindowsCapabilitySIDsConcurrentLoadOrCreateAgrees(t *testing.T) {
	const trials = 20
	const callers = 16
	for trial := 0; trial < trials; trial++ {
		home := filepath.Join(t.TempDir(), "sandbox")
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]WindowsCapabilitySIDs, callers)
		errs := make([]error, callers)
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				caps, err := LoadOrCreateWindowsCapabilitySIDs(home)
				results[i] = caps
				errs[i] = err
			}(i)
		}
		close(start)
		wg.Wait()
		for i := 0; i < callers; i++ {
			if errs[i] != nil {
				t.Fatalf("trial %d caller %d: LoadOrCreateWindowsCapabilitySIDs: %v", trial, i, errs[i])
			}
		}
		onDisk := readWindowsCapabilitySIDsFile(t, home)
		for i := 0; i < callers; i++ {
			if results[i].ReadOnly != onDisk.ReadOnly {
				t.Fatalf("trial %d caller %d read-only SID = %q, file on disk has %q: concurrent creators disagree",
					trial, i, results[i].ReadOnly, onDisk.ReadOnly)
			}
			if results[i].Offline != onDisk.Offline {
				t.Fatalf("trial %d caller %d offline SID = %q, file on disk has %q: concurrent creators disagree",
					trial, i, results[i].Offline, onDisk.Offline)
			}
		}
	}
}

// Concurrent per-root adds are load-add-save sequences: every caller reads the
// same map, adds only its own root, and the last save drops the others. All
// roots must survive and each caller must get back the stored SID.
func TestWindowsCapabilitySIDsConcurrentWorkspaceAddsKeepEveryRoot(t *testing.T) {
	home := filepath.Join(t.TempDir(), "sandbox")
	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	roots := make([]string, callers)
	returned := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		roots[i] = fmt.Sprintf(`C:\race\workspace-%02d`, i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sid, err := WindowsWorkspaceCapabilitySID(home, roots[i])
			returned[i] = sid
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d: WindowsWorkspaceCapabilitySID: %v", i, errs[i])
		}
	}
	onDisk := readWindowsCapabilitySIDsFile(t, home)
	for i := 0; i < callers; i++ {
		key := windowsCapabilityPathKey(roots[i])
		stored, ok := onDisk.WorkspaceByRoot[key]
		if !ok {
			t.Fatalf("root %q lost: %d of %d concurrent workspace adds missing from the file", roots[i], missingCount(onDisk.WorkspaceByRoot, roots), callers)
		}
		if stored != returned[i] {
			t.Fatalf("root %q stored SID = %q, caller got %q: a racing save overwrote it", roots[i], stored, returned[i])
		}
	}
}

// Same lost-update shape for the writable-root map.
func TestWindowsCapabilitySIDsConcurrentWritableRootAddsKeepEveryRoot(t *testing.T) {
	home := filepath.Join(t.TempDir(), "sandbox")
	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	roots := make([]string, callers)
	returned := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		roots[i] = fmt.Sprintf(`D:\race\cache-%02d`, i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sid, err := WindowsWritableRootCapabilitySID(home, roots[i])
			returned[i] = sid
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d: WindowsWritableRootCapabilitySID: %v", i, errs[i])
		}
	}
	onDisk := readWindowsCapabilitySIDsFile(t, home)
	for i := 0; i < callers; i++ {
		key := windowsCapabilityPathKey(roots[i])
		stored, ok := onDisk.WritableRootByPath[key]
		if !ok {
			t.Fatalf("root %q lost: %d of %d concurrent writable-root adds missing from the file", roots[i], missingCount(onDisk.WritableRootByPath, roots), callers)
		}
		if stored != returned[i] {
			t.Fatalf("root %q stored SID = %q, caller got %q: a racing save overwrote it", roots[i], stored, returned[i])
		}
	}
}

// The production shape: K separate processes (one per sandbox runner spawn)
// create and add on the same fresh home at once. On Windows the losers fail
// with a sharing violation while another process replaces the file; on every
// OS they mint disagreeing SIDs and drop each other's roots.
func TestWindowsCapabilitySIDsConcurrentAcrossProcessesAgrees(t *testing.T) {
	home := filepath.Join(t.TempDir(), "sandbox")
	resultsDir := filepath.Join(t.TempDir(), "results")
	if err := os.MkdirAll(resultsDir, 0o700); err != nil {
		t.Fatalf("create results dir: %v", err)
	}
	const children = 8
	type childResult struct {
		cmd *exec.Cmd
		err error
	}
	done := make(chan childResult, children)
	for i := 0; i < children; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsCapabilitySIDConcurrentHelperProcess$")
		cmd.Env = append(os.Environ(),
			"QG_WINDOWS_CAP_SID_HELPER=1",
			"QG_WINDOWS_CAP_SID_HOME="+home,
			"QG_WINDOWS_CAP_SID_INDEX="+fmt.Sprintf("%d", i),
			"QG_WINDOWS_CAP_SID_RESULTS="+resultsDir,
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
		go func(cmd *exec.Cmd) {
			done <- childResult{cmd: cmd, err: cmd.Wait()}
		}(cmd)
	}
	for i := 0; i < children; i++ {
		result := <-done
		if result.err != nil {
			t.Fatalf("helper process failed: %v", result.err)
		}
	}
	type helperReport struct {
		ReadOnly string `json:"readOnly"`
		Offline  string `json:"offline"`
		Root     string `json:"root"`
		SID      string `json:"sid"`
	}
	reports := make([]helperReport, children)
	for i := 0; i < children; i++ {
		bytes, err := os.ReadFile(filepath.Join(resultsDir, fmt.Sprintf("result-%d.json", i)))
		if err != nil {
			t.Fatalf("read helper %d report: %v", i, err)
		}
		if err := json.Unmarshal(bytes, &reports[i]); err != nil {
			t.Fatalf("parse helper %d report: %v", i, err)
		}
	}
	for i := 1; i < children; i++ {
		if reports[i].ReadOnly != reports[0].ReadOnly {
			t.Fatalf("helper %d read-only SID = %q, helper 0 got %q: processes disagree", i, reports[i].ReadOnly, reports[0].ReadOnly)
		}
		if reports[i].Offline != reports[0].Offline {
			t.Fatalf("helper %d offline SID = %q, helper 0 got %q: processes disagree", i, reports[i].Offline, reports[0].Offline)
		}
	}
	onDisk := readWindowsCapabilitySIDsFile(t, home)
	if onDisk.ReadOnly != reports[0].ReadOnly || onDisk.Offline != reports[0].Offline {
		t.Fatalf("file on disk has read-only %q offline %q, helpers agreed on %q %q",
			onDisk.ReadOnly, onDisk.Offline, reports[0].ReadOnly, reports[0].Offline)
	}
	for i := 0; i < children; i++ {
		key := windowsCapabilityPathKey(reports[i].Root)
		stored, ok := onDisk.WorkspaceByRoot[key]
		if !ok {
			t.Fatalf("helper %d root %q missing from the file: a racing save dropped it", i, reports[i].Root)
		}
		if stored != reports[i].SID {
			t.Fatalf("helper %d root %q stored SID = %q, helper got %q", i, reports[i].Root, stored, reports[i].SID)
		}
	}
}

// TestWindowsCapabilitySIDConcurrentHelperProcess is the child half of
// TestWindowsCapabilitySIDsConcurrentAcrossProcessesAgrees. It runs only when
// the parent selects it through the environment.
func TestWindowsCapabilitySIDConcurrentHelperProcess(t *testing.T) {
	if os.Getenv("QG_WINDOWS_CAP_SID_HELPER") == "" {
		t.Skip("helper process for TestWindowsCapabilitySIDsConcurrentAcrossProcessesAgrees")
	}
	home := os.Getenv("QG_WINDOWS_CAP_SID_HOME")
	index := os.Getenv("QG_WINDOWS_CAP_SID_INDEX")
	resultsDir := os.Getenv("QG_WINDOWS_CAP_SID_RESULTS")
	if home == "" || index == "" || resultsDir == "" {
		t.Fatal("QG_WINDOWS_CAP_SID_HOME, QG_WINDOWS_CAP_SID_INDEX and QG_WINDOWS_CAP_SID_RESULTS are required")
	}
	caps, err := LoadOrCreateWindowsCapabilitySIDs(home)
	if err != nil {
		t.Fatalf("helper %s: LoadOrCreateWindowsCapabilitySIDs: %v", index, err)
	}
	root := fmt.Sprintf(`C:\race\child-%s`, index)
	sid, err := WindowsWorkspaceCapabilitySID(home, root)
	if err != nil {
		t.Fatalf("helper %s: WindowsWorkspaceCapabilitySID: %v", index, err)
	}
	bytes, err := json.Marshal(map[string]string{
		"readOnly": caps.ReadOnly,
		"offline":  caps.Offline,
		"root":     root,
		"sid":      sid,
	})
	if err != nil {
		t.Fatalf("helper %s: marshal report: %v", index, err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "result-"+index+".json"), bytes, 0o600); err != nil {
		t.Fatalf("helper %s: write report: %v", index, err)
	}
}

func readWindowsCapabilitySIDsFile(t *testing.T, home string) WindowsCapabilitySIDs {
	t.Helper()
	bytes, err := os.ReadFile(WindowsCapabilitySIDPath(home))
	if err != nil {
		t.Fatalf("read capability SID file: %v", err)
	}
	var caps WindowsCapabilitySIDs
	if err := json.Unmarshal(bytes, &caps); err != nil {
		t.Fatalf("parse capability SID file: %v", err)
	}
	return caps
}

func missingCount(stored map[string]string, roots []string) int {
	missing := 0
	for _, root := range roots {
		if _, ok := stored[windowsCapabilityPathKey(root)]; !ok {
			missing++
		}
	}
	return missing
}
