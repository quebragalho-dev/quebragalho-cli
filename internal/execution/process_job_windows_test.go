//go:build windows

package execution

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestResumeProcessThreadsReportsFailureForUnknownPID(t *testing.T) {
	if ResumeProcessThreads(0) {
		t.Fatal("ResumeProcessThreads(0) = true, want false")
	}
	if ResumeProcessThreads(-1) {
		t.Fatal("ResumeProcessThreads(-1) = true, want false")
	}
}

func TestPrepareWindowsSuspendedProcessPreservesSysProcAttr(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "exit /b 0")
	attr := &syscall.SysProcAttr{HideWindow: true}
	cmd.SysProcAttr = attr

	PrepareWindowsSuspendedProcess(cmd)

	if cmd.SysProcAttr != attr {
		t.Fatal("PrepareWindowsSuspendedProcess replaced SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("PrepareWindowsSuspendedProcess cleared HideWindow")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_SUSPENDED set", cmd.SysProcAttr.CreationFlags)
	}
}

func TestWindowsProcessJobKillOnCloseLimitMatchesOptions(t *testing.T) {
	for _, wantKillOnClose := range []bool{false, true} {
		cmd := exec.Command("cmd", "/C", "exit /b 0")
		proc, err := StartWindowsProcessJob(cmd, WindowsJobOptions{KillOnJobClose: wantKillOnClose})
		if err != nil {
			t.Fatalf("StartWindowsProcessJob(KillOnJobClose=%v): %v", wantKillOnClose, err)
		}
		t.Cleanup(proc.Close)

		if proc.ProcessHandle() == 0 {
			t.Fatalf("KillOnJobClose=%v: ProcessHandle() = 0, want retained handle", wantKillOnClose)
		}
		job := proc.JobHandle()
		if job == 0 {
			t.Fatalf("KillOnJobClose=%v: JobHandle() = 0, want assigned Job Object", wantKillOnClose)
		}

		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		if err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
			nil,
		); err != nil {
			t.Fatalf("QueryInformationJobObject: %v", err)
		}
		hasFlag := info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0
		if hasFlag != wantKillOnClose {
			t.Fatalf("JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = %v, want %v", hasFlag, wantKillOnClose)
		}

		if err := cmd.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		var code uint32
		if err := windows.GetExitCodeProcess(proc.ProcessHandle(), &code); err != nil {
			t.Fatalf("GetExitCodeProcess after Wait: %v", err)
		}
		if code == stillActiveExitCode {
			t.Fatal("exit code = STILL_ACTIVE after Wait")
		}

		proc.Close()
		if proc.JobHandle() != 0 || proc.ProcessHandle() != 0 {
			t.Fatalf("Close left handles open: job=%v process=%v", proc.JobHandle(), proc.ProcessHandle())
		}
	}
}

func TestWindowsProcessJobFallsBackWhenJobCreationFails(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "exit /b 0")
	PrepareWindowsSuspendedProcess(cmd)
	proc := NewWindowsProcessJob(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(proc.Close)

	proc.attach(WindowsJobOptions{KillOnJobClose: true}, func(WindowsJobOptions) (windows.Handle, error) {
		return 0, errors.New("injected job creation failure")
	})

	if proc.JobHandle() != 0 {
		t.Fatalf("JobHandle() = %v, want 0 after injected failure", proc.JobHandle())
	}
	if proc.ProcessHandle() == 0 {
		t.Fatal("ProcessHandle() = 0, want retained handle for taskkill fallback")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait after job creation failure: %v", err)
	}
	if err := proc.Terminate(); err != nil {
		t.Fatalf("Terminate on fallback after Wait: %v", err)
	}
	proc.Close()
	if proc.ProcessHandle() != 0 {
		t.Fatalf("ProcessHandle() = %v after Close, want 0", proc.ProcessHandle())
	}
}
