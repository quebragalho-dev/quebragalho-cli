package background

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/envstr"
	"github.com/quebragalho-dev/quebragalho-cli/internal/fsutil"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusError     Status = "error"
	StatusKilled    Status = "killed"
)

// String renders the task status with its stable wire name.
func (s Status) String() string { return string(s) }

// Background task lifecycle as an explicit state machine.
//
//	[*] --> running: Register
//	running --> completed: UpdateStatus / MarkExited
//	running --> error: UpdateStatus / MarkExited / reload (normalizeLoadedTask)
//	running --> killed: Kill (optimistic mark, then signal)
//	killed --> running: restoreRunningAfterFailedKill only (failed Kill)
//	completed|error|killed --> same: idempotent no-op (UpdateStatus) / ignore (MarkExited)
//	completed|error|killed --> other: refused with error
//	running --> running: SetPID (pid change only)
//
// CompletedAt/ExitCode/PID are written only inside transitionLocked, which
// fixes the twins together: running always has zero CompletedAt and exit 0;
// killed always carries exit -1; completed/error always stamp CompletedAt.
// Load-time repair (normalizeLoadedTask) is construction, not a runtime
// transition: it already keeps the twins coherent (error, pid 0, exit -1).
type taskEvent int

const (
	taskEventUpdate taskEvent = iota
	taskEventExit
	taskEventKill
	taskEventRestore
	taskEventSetPID
)

// String renders the task event for tables and errors.
func (e taskEvent) String() string {
	switch e {
	case taskEventUpdate:
		return "update"
	case taskEventExit:
		return "exit"
	case taskEventKill:
		return "kill"
	case taskEventRestore:
		return "restore"
	case taskEventSetPID:
		return "setpid"
	default:
		return "unknown"
	}
}

// legalTaskTransition is the single table of legal task transitions. Every
// runtime state change goes through transitionLocked, which consults this
// table; anything not listed here is refused (or ignored, for MarkExited on
// a terminal task, preserving its historical no-op contract).
func legalTaskTransition(from Status, event taskEvent, to Status) bool {
	switch from {
	case StatusRunning:
		switch event {
		case taskEventUpdate:
			return to == StatusCompleted || to == StatusError || to == StatusRunning
		case taskEventExit:
			return to == StatusCompleted || to == StatusError
		case taskEventKill:
			return to == StatusKilled
		case taskEventRestore:
			return false
		case taskEventSetPID:
			return to == StatusRunning
		}
	case StatusCompleted, StatusError, StatusKilled:
		switch event {
		case taskEventUpdate:
			return to == from
		case taskEventExit:
			return to == StatusCompleted || to == StatusError
		case taskEventKill:
			return false
		case taskEventRestore:
			return from == StatusKilled && to == StatusRunning
		case taskEventSetPID:
			return false
		}
	}
	return false
}

type Task struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	SpecialistName string    `json:"specialistName,omitempty"`
	Description    string    `json:"description,omitempty"`
	ParentID       string    `json:"parentId,omitempty"`
	PID            int       `json:"pid,omitempty"`
	Status         Status    `json:"status"`
	OutputFile     string    `json:"outputFile"`
	StartedAt      time.Time `json:"startedAt"`
	CompletedAt    time.Time `json:"completedAt,omitempty"`
	ExitCode       int       `json:"exitCode,omitempty"`
}

type RegisterInput struct {
	TaskID         string
	Type           string
	SpecialistName string
	Description    string
	ParentID       string
	PID            int
	OutputFile     string
}

type ManagerOptions struct {
	RootDir     string
	Env         map[string]string
	Now         func() time.Time
	KillProcess func(pid int) error
}

type Manager struct {
	mu          sync.Mutex
	tasks       map[string]Task
	warnings    []string
	rootDir     string
	now         func() time.Time
	killProcess func(pid int) error
}

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func NewManager(rootDir string) (*Manager, error) {
	return NewManagerWithOptions(ManagerOptions{RootDir: rootDir})
}

func NewManagerWithOptions(options ManagerOptions) (*Manager, error) {
	rootDir := strings.TrimSpace(options.RootDir)
	if rootDir == "" {
		rootDir = DefaultRoot(options.Env)
	}
	rootDir = filepath.Clean(rootDir)
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create background task directory: %w", err)
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}
	killProcess := options.KillProcess
	if killProcess == nil {
		killProcess = terminateProcess
	}
	manager := &Manager{tasks: map[string]Task{}, rootDir: rootDir, now: now, killProcess: killProcess}
	if err := manager.loadTasks(); err != nil {
		return nil, err
	}
	return manager, nil
}

func DefaultRoot(env map[string]string) string {
	dataHome := strings.TrimSpace(envstr.Value(env, "XDG_DATA_HOME"))
	home := strings.TrimSpace(envstr.Value(env, "HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = userHome
		}
	}
	base := dataHome
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "qg", "background")
}

func (manager *Manager) RootDir() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.rootDir
}

func (manager *Manager) LoadWarnings() []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return append([]string(nil), manager.warnings...)
}

func (manager *Manager) Register(input RegisterInput) (string, error) {
	taskID := strings.TrimSpace(input.TaskID)
	if !validTaskID(taskID) {
		return "", fmt.Errorf("invalid background task id %q", input.TaskID)
	}
	taskType := strings.TrimSpace(input.Type)
	if taskType == "" {
		return "", fmt.Errorf("background task %s requires a type", taskID)
	}
	if input.PID < 0 {
		return "", fmt.Errorf("invalid background task pid %d", input.PID)
	}
	outputFile, err := manager.outputFile(taskID, input.OutputFile)
	if err != nil {
		return "", err
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.tasks[taskID]; exists {
		return "", fmt.Errorf("background task already registered: %s", taskID)
	}
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		return "", fmt.Errorf("create background task output directory: %w", err)
	}
	file, err := os.OpenFile(outputFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("background task output already exists: %s", outputFile)
		}
		return "", fmt.Errorf("create background task output file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close background task output file: %w", err)
	}

	task := Task{
		ID:             taskID,
		Type:           taskType,
		SpecialistName: strings.TrimSpace(input.SpecialistName),
		Description:    strings.TrimSpace(input.Description),
		ParentID:       strings.TrimSpace(input.ParentID),
		PID:            input.PID,
		Status:         StatusRunning,
		OutputFile:     outputFile,
		StartedAt:      manager.now(),
	}
	manager.tasks[taskID] = task
	if err := manager.persistTaskLocked(task); err != nil {
		delete(manager.tasks, taskID)
		_ = os.Remove(outputFile)
		return "", err
	}
	return outputFile, nil
}

func (manager *Manager) SetPID(taskID string, pid int) error {
	taskID = strings.TrimSpace(taskID)
	if pid <= 0 {
		return fmt.Errorf("invalid background task pid %d", pid)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, err := manager.transitionLocked(taskID, taskEventSetPID, StatusRunning, 0, pid)
	return err
}

// transitionLocked is the single point that writes the task state fields
// (Status, PID, CompletedAt, ExitCode). Callers hold manager.mu. It returns
// applied=true when the task was mutated and persisted; applied=false with a
// nil error is a legal no-op (idempotent same-terminal update, ignored
// MarkExited on a terminal task, or a stale restore). Illegal transitions
// return an error and leave the task — including its persisted JSON —
// untouched.
func (manager *Manager) transitionLocked(taskID string, event taskEvent, to Status, exitCode int, pid int) (bool, error) {
	task, ok := manager.tasks[taskID]
	if !ok {
		return false, fmt.Errorf("background task not found: %s", taskID)
	}
	from := task.Status
	if !legalTaskTransition(from, event, to) {
		if event == taskEventExit && validStatus(to) {
			// Historical MarkExited contract: never clobber a terminal
			// task, silently. Callers already rely on the nil return.
			return false, nil
		}
		if from != StatusRunning {
			return false, fmt.Errorf("background task %s is %s", taskID, from)
		}
		return false, fmt.Errorf("background task %s cannot transition from %s to %s", taskID, from, to)
	}
	switch {
	case from == StatusRunning && (event == taskEventUpdate || event == taskEventExit) &&
		(to == StatusCompleted || to == StatusError):
		task.Status = to
		task.ExitCode = exitCode
		if task.CompletedAt.IsZero() {
			task.CompletedAt = manager.now()
		}
	case from == StatusRunning && event == taskEventUpdate && to == StatusRunning:
		if task.ExitCode == 0 && task.CompletedAt.IsZero() {
			return false, nil
		}
		task.ExitCode = 0
		task.CompletedAt = time.Time{}
	case from == StatusRunning && event == taskEventKill && to == StatusKilled:
		if task.PID != pid {
			return false, fmt.Errorf("background task %s pid changed before kill completed", taskID)
		}
		task.Status = StatusKilled
		task.ExitCode = -1
		if task.CompletedAt.IsZero() {
			task.CompletedAt = manager.now()
		}
	case from == StatusKilled && event == taskEventRestore && to == StatusRunning:
		if task.PID != pid {
			return false, nil
		}
		task.Status = StatusRunning
		task.ExitCode = 0
		task.CompletedAt = time.Time{}
	case event == taskEventSetPID && to == StatusRunning:
		if task.PID == pid {
			return false, nil
		}
		task.PID = pid
	case event == taskEventUpdate && from != StatusRunning && to == from:
		return false, nil
	case event == taskEventExit:
		return false, nil
	default:
		return false, fmt.Errorf("background task %s cannot transition from %s to %s", taskID, from, to)
	}
	if err := manager.persistTaskLocked(task); err != nil {
		return false, err
	}
	manager.tasks[taskID] = task
	return true, nil
}

func (manager *Manager) UpdateStatus(taskID string, status Status, exitCode int) error {
	taskID = strings.TrimSpace(taskID)
	if !validStatus(status) {
		return fmt.Errorf("invalid background task status %q", status)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, err := manager.transitionLocked(taskID, taskEventUpdate, status, exitCode, 0)
	return err
}

func (manager *Manager) MarkExited(taskID string, status Status, exitCode int) error {
	taskID = strings.TrimSpace(taskID)
	if status != StatusCompleted && status != StatusError {
		return fmt.Errorf("invalid background task exit status %q", status)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, err := manager.transitionLocked(taskID, taskEventExit, status, exitCode, 0)
	return err
}

func (manager *Manager) Get(taskID string) (Task, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	task, ok := manager.tasks[strings.TrimSpace(taskID)]
	return task, ok
}

func (manager *Manager) Kill(taskID string) error {
	taskID = strings.TrimSpace(taskID)

	pid, err := manager.killTarget(taskID)
	if err != nil {
		return err
	}
	if !manager.isRunningPID(taskID, pid) {
		return nil
	}
	// Record the kill intent BEFORE terminating. terminateProcess can block until
	// the child exits, during which the background Wait-goroutine may reap it and
	// call MarkExited; marking killed first makes that MarkExited a no-op (it only
	// acts on a running task), so a user-initiated stop stays "killed" instead of
	// being clobbered to "error".
	marked, err := manager.markKilledIfStillRunning(taskID, pid)
	if err != nil {
		return err
	}
	if !marked {
		// The task exited (or was reused) between the running check and now — the
		// pid may be stale, so do NOT signal it.
		return nil
	}
	if err := manager.killProcess(pid); err != nil {
		// Couldn't terminate — undo the optimistic kill mark so the still-running
		// task is not falsely reported as killed.
		killErr := fmt.Errorf("kill background task %s: %w", taskID, err)
		if restoreErr := manager.restoreRunningAfterFailedKill(taskID, pid); restoreErr != nil {
			return errors.Join(killErr, fmt.Errorf("restore running state for %s: %w", taskID, restoreErr))
		}
		return killErr
	}
	return nil
}

func (manager *Manager) KillRunning() error {
	var errs []error
	for _, task := range manager.List() {
		if task.Status != StatusRunning {
			continue
		}
		if err := manager.Kill(task.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (manager *Manager) killTarget(taskID string) (int, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	task, ok := manager.tasks[taskID]
	if !ok {
		return 0, fmt.Errorf("background task not found: %s", taskID)
	}
	if task.Status != StatusRunning {
		return 0, fmt.Errorf("background task %s is %s", taskID, task.Status)
	}
	if task.PID <= 0 {
		return 0, fmt.Errorf("background task %s has no pid", taskID)
	}
	return task.PID, nil
}

func (manager *Manager) isRunningPID(taskID string, pid int) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	task, ok := manager.tasks[taskID]
	return ok && task.Status == StatusRunning && task.PID == pid
}

// markKilledIfStillRunning marks the task killed if it is still running. The
// bool reports whether it actually marked: false means the task already exited
// (or was reused), in which case the caller must NOT signal the pid — it may be
// stale.
func (manager *Manager) markKilledIfStillRunning(taskID string, pid int) (bool, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	task, ok := manager.tasks[taskID]
	if !ok {
		return false, fmt.Errorf("background task not found: %s", taskID)
	}
	if task.Status != StatusRunning {
		return false, nil
	}
	return manager.transitionLocked(taskID, taskEventKill, StatusKilled, -1, pid)
}

// restoreRunningAfterFailedKill reverts a task that was optimistically marked
// killed back to running when the terminate failed (the process is presumed
// still alive). It only touches a task still killed with the same pid, so a real
// exit recorded meanwhile is left untouched. It returns the persistence error so
// the caller can surface a failed rollback rather than leaving the task wrongly
// marked killed.
func (manager *Manager) restoreRunningAfterFailedKill(taskID string, pid int) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, ok := manager.tasks[taskID]; !ok {
		return nil
	}
	_, err := manager.transitionLocked(taskID, taskEventRestore, StatusRunning, 0, pid)
	return err
}

func (manager *Manager) loadTasks() error {
	entries, err := os.ReadDir(manager.rootDir)
	if err != nil {
		return fmt.Errorf("read background task directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			manager.warnf("skipped symlink background task metadata: %s", filepath.Join(manager.rootDir, entry.Name()))
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".json")
		if !validTaskID(taskID) {
			manager.warnf("skipped invalid background task metadata file %q", entry.Name())
			continue
		}
		path := manager.metadataFile(taskID)
		data, err := os.ReadFile(path)
		if err != nil {
			manager.warnf("skipped unreadable background task metadata %s: %s", path, err)
			continue
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			manager.warnf("skipped invalid background task metadata %s: %s", path, err)
			continue
		}
		task, changed, err := manager.normalizeLoadedTask(taskID, task)
		if err != nil {
			manager.warnf("skipped invalid background task metadata %s: %s", path, err)
			continue
		}
		if changed {
			if err := manager.persistTaskLocked(task); err != nil {
				manager.warnf("failed to repair background task metadata %s: %s", path, err)
			}
		}
		manager.tasks[task.ID] = task
	}
	return nil
}

func (manager *Manager) normalizeLoadedTask(fileTaskID string, task Task) (Task, bool, error) {
	changed := false
	task.ID = strings.TrimSpace(task.ID)
	if task.ID == "" {
		task.ID = fileTaskID
		changed = true
	}
	if task.ID != fileTaskID {
		return Task{}, false, fmt.Errorf("metadata id %q does not match file id %q", task.ID, fileTaskID)
	}
	if !validTaskID(task.ID) {
		return Task{}, false, fmt.Errorf("invalid task id %q", task.ID)
	}
	if trimmed := strings.TrimSpace(task.Type); trimmed != task.Type {
		task.Type = trimmed
		changed = true
	}
	if task.Type == "" {
		return Task{}, false, fmt.Errorf("background task %s requires a type", task.ID)
	}
	if !validStatus(task.Status) {
		return Task{}, false, fmt.Errorf("invalid background task status %q", task.Status)
	}
	if task.PID < 0 {
		return Task{}, false, fmt.Errorf("invalid background task pid %d", task.PID)
	}
	outputFile, err := manager.outputFile(task.ID, task.OutputFile)
	if err != nil {
		var migrated bool
		outputFile, migrated = manager.migrateLegacyOutputFile(task.ID, task.OutputFile)
		if !migrated {
			return Task{}, false, err
		}
		changed = true
	}
	if outputFile != task.OutputFile {
		changed = true
	}
	task.OutputFile = outputFile
	if task.Status == StatusRunning {
		task.Status = StatusError
		task.PID = 0
		task.ExitCode = -1
		if task.CompletedAt.IsZero() {
			task.CompletedAt = manager.now()
		}
		changed = true
		manager.warnf("marked reloaded running background task %s as error; original process ownership was lost", task.ID)
	}
	return task, changed, nil
}

func (manager *Manager) persistTaskLocked(task Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("encode background task metadata: %w", err)
	}
	path := manager.metadataFile(task.ID)
	// Durable publish: fsync before the atomic rename (with retry on transient
	// Windows sharing violations) so a power loss cannot leave an empty file.
	if err := fsutil.WriteFileAtomically(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("replace background task metadata: %w", err)
	}
	return nil
}

func (manager *Manager) metadataFile(taskID string) string {
	return filepath.Join(manager.rootDir, taskID+".json")
}

func (manager *Manager) warnf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	manager.warnings = append(manager.warnings, message)
	log.Printf("qg background: %s", message)
}

func (manager *Manager) OutputPath(taskID string) string {
	task, ok := manager.Get(taskID)
	if !ok {
		return ""
	}
	return task.OutputFile
}

func (manager *Manager) ListByParent(parentID string) []Task {
	parentID = strings.TrimSpace(parentID)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	tasks := []Task{}
	for _, task := range manager.tasks {
		if task.ParentID == parentID {
			tasks = append(tasks, task)
		}
	}
	sortTasks(tasks)
	return tasks
}

func (manager *Manager) List() []Task {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	tasks := make([]Task, 0, len(manager.tasks))
	for _, task := range manager.tasks {
		tasks = append(tasks, task)
	}
	sortTasks(tasks)
	return tasks
}

func (manager *Manager) outputFile(taskID string, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return filepath.Join(manager.rootDir, taskID+".ndjson"), nil
	}
	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(manager.rootDir, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(manager.rootDir, path)
	if err != nil {
		return "", fmt.Errorf("resolve background task output file: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("background task output file must be inside %s", manager.rootDir)
	}
	return path, nil
}

// migrateLegacyOutputFile recovers task metadata written under a previous home
// or XDG data root. It accepts only the canonical per-task output
// name in the historic qg/background layout, and only when the corresponding
// regular file already exists inside the active manager root.
func (manager *Manager) migrateLegacyOutputFile(taskID string, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if !filepath.IsAbs(requested) {
		return "", false
	}
	requested = filepath.Clean(requested)
	expectedName := taskID + ".ndjson"
	if filepath.Base(requested) != expectedName {
		return "", false
	}
	legacyRoot := filepath.Dir(requested)
	if filepath.Base(legacyRoot) != "background" || filepath.Base(filepath.Dir(legacyRoot)) != "qg" {
		return "", false
	}
	canonical := filepath.Join(manager.rootDir, expectedName)
	info, err := os.Lstat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return canonical, true
}

func validTaskID(taskID string) bool {
	return taskIDPattern.MatchString(strings.TrimSpace(taskID))
}

func validStatus(status Status) bool {
	switch status {
	case StatusRunning, StatusCompleted, StatusError, StatusKilled:
		return true
	default:
		return false
	}
}

func sortTasks(tasks []Task) {
	sort.SliceStable(tasks, func(left int, right int) bool {
		if tasks[left].StartedAt.Equal(tasks[right].StartedAt) {
			return tasks[left].ID < tasks[right].ID
		}
		return tasks[left].StartedAt.After(tasks[right].StartedAt)
	})
}
