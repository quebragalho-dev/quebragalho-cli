//go:build windows

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActiveExitCode = uint32(259) // STILL_ACTIVE

// WindowsJobOptions configures how a Windows command process is attached to a Job Object.
type WindowsJobOptions struct {
	// KillOnJobClose sets JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE on the Job Object so
	// closing the last handle terminates every remaining process in the job.
	KillOnJobClose bool
}

// WindowsProcessJob tracks a started Windows command, its retained process
// handle (preventing PID reuse before termination), and an optional Job Object
// containing the command and descendants spawned after the main thread resumes.
type WindowsProcessJob struct {
	mu            sync.Mutex
	cmd           *exec.Cmd
	job           windows.Handle
	processHandle windows.Handle
	terminated    bool
	closed        bool
}

// PrepareWindowsSuspendedProcess configures cmd to start with its main thread
// suspended so it can be assigned to a Job Object before executing any code.
// Without this, a fast command can spawn and detach a descendant before
// AssignProcessToJobObject runs, letting that descendant escape the job.
func PrepareWindowsSuspendedProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// NewWindowsProcessJob creates a tracker for cmd before Start is called, so
// cmd.Cancel can safely call Terminate even if cancellation races with Attach.
func NewWindowsProcessJob(cmd *exec.Cmd) *WindowsProcessJob {
	return &WindowsProcessJob{cmd: cmd}
}

// StartWindowsProcessJob prepares cmd to start suspended, starts it, attaches
// it to a Job Object according to opts, and resumes its main thread.
func StartWindowsProcessJob(cmd *exec.Cmd, opts WindowsJobOptions) (*WindowsProcessJob, error) {
	PrepareWindowsSuspendedProcess(cmd)
	proc := NewWindowsProcessJob(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc.Attach(opts)
	return proc, nil
}

// Attach opens a process handle for cmd, creates and assigns a Job Object
// according to opts, and resumes the suspended main thread. If job creation or
// assignment fails, it retains processHandle for taskkill /T fallback. If
// resuming fails, it terminates the process immediately so it never sits
// suspended indefinitely.
func (p *WindowsProcessJob) Attach(opts WindowsJobOptions) {
	p.attach(opts, createWindowsJobObject)
}

func (p *WindowsProcessJob) attach(opts WindowsJobOptions, createJob func(WindowsJobOptions) (windows.Handle, error)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || p.terminated || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	pid := p.cmd.Process.Pid

	// The main thread is suspended (see PrepareWindowsSuspendedProcess); resume
	// it once we're done attaching, however that turns out. If resuming fails
	// outright (Toolhelp/OpenThread/ResumeThread errors are otherwise
	// swallowed), the process would sit suspended forever and never exit on its
	// own, making the failure look like a command timeout. Terminate it
	// immediately instead so the real cause surfaces quickly and no suspended
	// process is left behind.
	defer func() {
		if !ResumeProcessThreads(pid) {
			_ = p.terminateLocked()
		}
	}()

	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if err != nil {
		return
	}
	p.processHandle = handle

	job, err := createJob(opts)
	if err != nil {
		return
	}
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	p.job = job
}

func createWindowsJobObject(opts WindowsJobOptions) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	if opts.KillOnJobClose {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(job)
			return 0, err
		}
	}
	return job, nil
}

// ResumeProcessThreads resumes every (assumed suspended) thread of pid and
// reports whether at least one was actually resumed. Callers must treat a false
// result as a failed resume, not a benign no-op: a suspended process that's
// never woken up will neither exit nor produce output on its own.
func ResumeProcessThreads(pid int) bool {
	if pid <= 0 {
		return false
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := false
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			continue
		}
		if _, err := windows.ResumeThread(thread); err == nil {
			resumed = true
		}
		_ = windows.CloseHandle(thread)
	}
	return resumed
}

// Terminate stops the tracked process tree. When a Job Object is attached,
// TerminateJobObject kills every member atomically. Otherwise it falls back to
// taskkill /T while the retained process handle confirms the root is still
// active (preventing PID reuse), plus TerminateProcess / Process.Kill.
func (p *WindowsProcessJob) Terminate() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminateLocked()
}

func (p *WindowsProcessJob) terminateLocked() error {
	if p.closed || p.terminated {
		return nil
	}
	p.terminated = true
	if p.job != 0 {
		// A job handle means the process tree was contained from launch, so
		// TerminateJobObject alone kills every member atomically. Don't also
		// fall through to the taskkill/PID path below: by the time Terminate
		// runs on the ErrWaitDelay path, cmd.Wait may have already reaped the
		// root process, so its PID could have been reused by an unrelated
		// process.
		return windows.TerminateJobObject(p.job, 1)
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if p.processHandle == 0 {
		// Without a retained identity, never target a numeric PID that may
		// have been reaped and reused. os.Process.Kill uses Go's original
		// process state instead.
		err := p.cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	// Fallback for the rare case where job creation or assignment failed:
	// only invoke taskkill while the identity-preserving handle confirms the
	// original root is still active. Keeping that handle open until Close
	// prevents Windows from reusing its PID for an unrelated process.
	var exitCode uint32
	if err := windows.GetExitCodeProcess(p.processHandle, &exitCode); err == nil {
		if exitCode != stillActiveExitCode {
			return nil
		}
		taskkill := taskkillPath()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		treeErr := exec.CommandContext(ctx, taskkill, "/T", "/F", "/PID", strconv.Itoa(p.cmd.Process.Pid)).Run()
		cancel()
		var afterCode uint32
		if err := windows.GetExitCodeProcess(p.processHandle, &afterCode); err == nil && afterCode != stillActiveExitCode {
			return nil
		}
		termErr := windows.TerminateProcess(p.processHandle, 1)
		if errors.Is(termErr, windows.ERROR_ACCESS_DENIED) {
			if err := windows.GetExitCodeProcess(p.processHandle, &afterCode); err == nil && afterCode != stillActiveExitCode {
				return nil
			}
		}
		return errors.Join(treeErr, termErr)
	}
	return windows.TerminateProcess(p.processHandle, 1)
}

// Close releases the retained Job Object and process handles. When
// KillOnJobClose was set, closing the job handle also terminates any remaining
// processes in the job; otherwise detached descendants keep running unless
// Terminate was called explicitly.
func (p *WindowsProcessJob) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.job != 0 {
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
	if p.processHandle != 0 {
		_ = windows.CloseHandle(p.processHandle)
		p.processHandle = 0
	}
}

// JobHandle returns the retained Job Object handle, or 0 if unavailable or closed.
func (p *WindowsProcessJob) JobHandle() windows.Handle {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.job
}

// ProcessHandle returns the retained process handle, or 0 if unavailable or closed.
func (p *WindowsProcessJob) ProcessHandle() windows.Handle {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.processHandle
}
