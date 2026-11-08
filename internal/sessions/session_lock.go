package sessions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// presenceLockName is a separate lock file from session.lock: the per-op
// session.lock is taken and released around every mutation, so a
// session-long lease must live on its own file or same-process appends
// would deadlock against it.
const presenceLockName = "presence.lock"

// SessionLockedError reports a refusal to open a session that another live
// qg process holds. The PID is best-effort: the OS lock is authoritative
// (a dead holder's lock is released by the kernel), so a stale PID file
// never blocks a new owner.
type SessionLockedError struct {
	SessionID string
	PID       int
}

func (err *SessionLockedError) Error() string {
	if err == nil {
		return ""
	}
	if err.PID > 0 {
		return fmt.Sprintf("Esta sessão está aberta em outro terminal (PID %d). Feche lá ou use /fork.", err.PID)
	}
	return "Esta sessão está aberta em outro terminal. Feche lá ou use /fork."
}

// SessionLock is a held session presence lease. Close it (idempotent) when
// the session is switched away from or the process exits; the OS releases a
// leaked lock automatically on process death.
//
// The lease is process-wide: a second acquire of the same lock file in the
// same process (another model instance in tests, the CLI-to-TUI handoff)
// shares the single OS lock via reference counting instead of conflicting
// with itself. A second *process* still gets a *SessionLockedError: the OS
// flock is authoritative, and a dead holder's kernel-released lock never
// blocks a new owner.
type SessionLock struct {
	store     *Store
	sessionID string
	path      string
	file      *os.File
	release   func()
	closed    bool
}

// SessionID reports the session this lock holds.
func (lock *SessionLock) SessionID() string {
	if lock == nil {
		return ""
	}
	return lock.sessionID
}

// Release drops the lease. Idempotent; safe to call on a nil lock.
// With process-wide sharing, each holder releases its own reference and
// only the last one out unlocks the file.
func (lock *SessionLock) Release() {
	if lock == nil || lock.closed {
		return
	}
	lock.closed = true
	presenceRegistry.Lock()
	holder, ok := presenceRegistry.held[lock.path]
	switch {
	case !ok || holder.file != lock.file:
		// Not the shared holder (defensive): release directly.
		presenceRegistry.Unlock()
		if lock.release != nil {
			lock.release()
		}
		if lock.file != nil {
			_ = lock.file.Close()
		}
		return
	case holder.refs <= 1:
		delete(presenceRegistry.held, lock.path)
		presenceRegistry.Unlock()
		if lock.release != nil {
			lock.release()
		}
		if lock.file != nil {
			_ = lock.file.Close()
		}
		return
	default:
		holder.refs--
		presenceRegistry.Unlock()
		return
	}
}

// presenceRegistry tracks the presence locks held by this process, keyed by
// lock-file path. Same-path acquires share one OS lock (ref-counted); the
// kernel still serializes against other processes.
var presenceRegistry = struct {
	sync.Mutex
	held map[string]*presenceHolder
}{held: map[string]*presenceHolder{}}

type presenceHolder struct {
	file    *os.File
	release func()
	refs    int
}

func (store *Store) presenceLockPath(sessionID string) string {
	return filepath.Join(store.sessionPath(sessionID), presenceLockName)
}

// TryAcquireSessionLock takes the session presence lease without blocking.
// A second holder — another terminal, or the same process through another
// open description — gets a *SessionLockedError naming the recorded PID.
func (store *Store) TryAcquireSessionLock(sessionID string) (*SessionLock, error) {
	if !ValidSessionID(sessionID) {
		return nil, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	if err := os.MkdirAll(store.sessionPath(sessionID), 0o700); err != nil {
		return nil, fmt.Errorf("create qg session directory: %w", err)
	}
	path := store.presenceLockPath(sessionID)
	// Same-process re-acquire: share the already-held OS lock instead of
	// conflicting with ourselves (tests open several models on one store;
	// the CLI-to-TUI handoff adopts the CLI's lease the same way).
	presenceRegistry.Lock()
	if holder, ok := presenceRegistry.held[path]; ok {
		holder.refs++
		presenceRegistry.Unlock()
		return &SessionLock{store: store, sessionID: sessionID, path: path, file: holder.file, release: holder.release}, nil
	}
	presenceRegistry.Unlock()
	release, file, err := tryAcquireFileLock(path)
	if err != nil {
		holder := 0
		if data, readErr := os.ReadFile(path); readErr == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				holder = pid
			}
		}
		if errors.Is(err, errSessionLockHeld) {
			return nil, &SessionLockedError{SessionID: sessionID, PID: holder}
		}
		return nil, err
	}
	pidText := strconv.Itoa(os.Getpid())
	if _, err := file.WriteAt([]byte(pidText), 0); err != nil {
		release()
		_ = file.Close()
		return nil, fmt.Errorf("write qg session presence: %w", err)
	}
	_ = file.Truncate(int64(len(pidText)))
	presenceRegistry.Lock()
	presenceRegistry.held[path] = &presenceHolder{file: file, release: release, refs: 1}
	presenceRegistry.Unlock()
	return &SessionLock{store: store, sessionID: sessionID, path: path, file: file, release: release}, nil
}
