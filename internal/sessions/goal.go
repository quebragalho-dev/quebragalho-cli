package sessions

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	GoalObjectiveMaxLength            = 4_000
	GoalMaxConsecutiveContinuations   = 20
	goalContinuationLimitStatusReason = "automatic continuation limit reached"
)

func validateGoalObjective(objective string) (string, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "", fmt.Errorf("goal objective is required")
	}
	if utf8.RuneCountInString(objective) > GoalObjectiveMaxLength {
		return "", fmt.Errorf("goal objective cannot exceed %d characters", GoalObjectiveMaxLength)
	}
	return objective, nil
}

// CreateGoal persists a new active goal for a session. It refuses to replace an
// existing goal so callers must make replacement an explicit user decision.
func (store *Store) CreateGoal(sessionID, objective string, tokenBudget int) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	var err error
	objective, err = validateGoalObjective(objective)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if tokenBudget < 0 {
		return Metadata{}, Event{}, fmt.Errorf("goal token budget cannot be negative")
	}
	objective, tasks := splitGoalObjective(objective)

	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal != nil {
		return Metadata{}, Event{}, fmt.Errorf("session already has a goal")
	}
	now := store.timestamp()
	session.Goal = &Goal{
		Objective:         objective,
		Tasks:             tasks,
		Status:            GoalStatusActive,
		TokenBudget:       tokenBudget,
		ContinuationLimit: GoalMaxConsecutiveContinuations,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalCreated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// UpdateGoal changes a goal's lifecycle state. Terminal goals remain available
// for status/history until the user explicitly clears them.
func (store *Store) UpdateGoal(sessionID string, status GoalStatus, reason string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	if !validGoalStatus(status) {
		return Metadata{}, Event{}, fmt.Errorf("invalid goal status %q", status)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	if status == GoalStatusComplete && GoalTasksPending(session.Goal) {
		done, total := GoalTaskProgress(session.Goal)
		return Metadata{}, Event{}, fmt.Errorf("cannot complete goal with %d of %d tasks pending: complete the current task first", total-done, total)
	}
	session.Goal.Status = status
	session.Goal.StatusReason = strings.TrimSpace(reason)
	if status == GoalStatusActive {
		session.Goal.ContinuationCount = 0
		if session.Goal.ContinuationLimit <= 0 {
			session.Goal.ContinuationLimit = GoalMaxConsecutiveContinuations
		}
	}
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// EditGoal replaces the objective and optional budget without resetting usage or
// lifecycle timestamps. User-facing command handling owns confirmation.
func (store *Store) EditGoal(sessionID, objective string, tokenBudget int) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	var err error
	objective, err = validateGoalObjective(objective)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if tokenBudget < 0 {
		return Metadata{}, Event{}, fmt.Errorf("goal token budget cannot be negative")
	}
	objective, tasks := splitGoalObjective(objective)
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	session.Goal.Objective = objective
	session.Goal.Tasks = tasks
	session.Goal.TokenBudget = tokenBudget
	session.Goal.Status = GoalStatusActive
	session.Goal.StatusReason = ""
	session.Goal.ContinuationCount = 0
	if session.Goal.ContinuationLimit <= 0 {
		session.Goal.ContinuationLimit = GoalMaxConsecutiveContinuations
	}
	if tokenBudget > 0 && session.Goal.TokensUsed >= tokenBudget {
		session.Goal.Status = GoalStatusBudgetLimited
		session.Goal.StatusReason = "token budget reached"
	}
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err = store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// ResetGoalContinuations starts a fresh autonomous-run allowance after explicit
// user input. It makes the safety bound consecutive rather than lifetime-wide.
func (store *Store) ResetGoalContinuations(sessionID string) (Metadata, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, err
	}
	if session.Goal == nil || session.Goal.Status != GoalStatusActive {
		return session, nil
	}
	changed := false
	if session.Goal.ContinuationCount != 0 {
		session.Goal.ContinuationCount = 0
		changed = true
	}
	if session.Goal.ContinuationLimit <= 0 {
		session.Goal.ContinuationLimit = GoalMaxConsecutiveContinuations
		changed = true
	}
	if !changed {
		return session, nil
	}
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, err
	}
	return session, nil
}

// ReserveGoalContinuation atomically reserves one automatic continuation. Once
// the persisted consecutive-run limit is exhausted it pauses the goal before
// another provider request can start, even when the provider reports no usage.
func (store *Store) ReserveGoalContinuation(sessionID string) (Metadata, *Event, bool, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, nil, false, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, nil, false, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, false, err
	}
	if session.Goal == nil || session.Goal.Status != GoalStatusActive {
		return session, nil, false, nil
	}
	limit := session.Goal.ContinuationLimit
	if limit <= 0 {
		limit = GoalMaxConsecutiveContinuations
		session.Goal.ContinuationLimit = limit
	}
	if session.Goal.ContinuationCount < limit {
		session.Goal.ContinuationCount++
		session.Goal.UpdatedAt = store.timestamp()
		if err := store.writeMetadata(session); err != nil {
			return Metadata{}, nil, false, err
		}
		return session, nil, true, nil
	}

	session.Goal.Status = GoalStatusPaused
	session.Goal.StatusReason = goalContinuationLimitStatusReason
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, nil, false, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, nil, false, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, false, err
	}
	return loaded, &event, false, nil
}

// AddGoalUsage accounts tokens consumed while a goal is active. Reaching the
// optional budget pauses the goal before another autonomous turn can start.
func (store *Store) AddGoalUsage(sessionID string, tokens int) (Metadata, *Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, nil, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	if tokens < 0 {
		return Metadata{}, nil, fmt.Errorf("goal token usage cannot be negative")
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	if session.Goal == nil || tokens == 0 {
		return session, nil, nil
	}
	session.Goal.TokensUsed += tokens
	session.Goal.UpdatedAt = store.timestamp()
	budgetReached := false
	if session.Goal.Status == GoalStatusActive &&
		session.Goal.TokenBudget > 0 &&
		session.Goal.TokensUsed >= session.Goal.TokenBudget {
		session.Goal.Status = GoalStatusBudgetLimited
		session.Goal.StatusReason = "token budget reached"
		budgetReached = true
	}
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, nil, err
	}
	if !budgetReached {
		return session, nil, nil
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, nil, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	return loaded, &event, nil
}

// PauseGoalIfActive pauses a goal only while it is still active. It is used by
// cancellation paths where an in-flight agent may have completed or blocked the
// goal immediately before the cancellation reached the runtime.
func (store *Store) PauseGoalIfActive(sessionID, reason string) (Metadata, *Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, nil, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	if session.Goal == nil || session.Goal.Status != GoalStatusActive {
		return session, nil, nil
	}
	session.Goal.Status = GoalStatusPaused
	session.Goal.StatusReason = strings.TrimSpace(reason)
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, nil, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, nil, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, nil, err
	}
	return loaded, &event, nil
}

// ClearGoal removes the current goal while preserving an audit event.
func (store *Store) ClearGoal(sessionID string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	cleared := cloneGoal(session.Goal)
	session.Goal = nil
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalCleared,
		Payload: cleared,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// splitGoalObjective parses a task list out of a goal body. Plain
// single-sentence objectives come back unchanged with no tasks, so goals
// written before task support keep working.
func splitGoalObjective(objective string) (string, []GoalTask) {
	parsed, tasks := ParseGoalText(objective)
	if len(tasks) == 0 {
		return objective, nil
	}
	return parsed, tasks
}

// SetGoalTasks replaces the goal checklist with the given titles (2-12). The
// first task becomes active and the continuation allowance restarts, so the
// next automatic run works on a fresh task. It refuses to overwrite an
// existing list so a split is an explicit decision.
func (store *Store) SetGoalTasks(sessionID string, titles []string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	titles, err := validateGoalTaskTitles(titles)
	if err != nil {
		return Metadata{}, Event{}, err
	}

	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	if len(session.Goal.Tasks) > 0 {
		return Metadata{}, Event{}, fmt.Errorf("goal already has tasks")
	}
	session.Goal.Tasks = make([]GoalTask, len(titles))
	for i, title := range titles {
		status := GoalTaskPending
		if i == 0 {
			status = GoalTaskActive
		}
		session.Goal.Tasks[i] = GoalTask{
			ID:     fmt.Sprintf("t%d", i+1),
			Title:  title,
			Status: status,
		}
	}
	session.Goal.ContinuationCount = 0
	if session.Goal.ContinuationLimit <= 0 {
		session.Goal.ContinuationLimit = GoalMaxConsecutiveContinuations
	}
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err = store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// CompleteGoalTask marks the current task done with an optional note and
// opens the next pending task with a fresh continuation allowance. When no
// task remains, the whole goal completes.
func (store *Store) CompleteGoalTask(sessionID, note string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	if len(session.Goal.Tasks) == 0 {
		return Metadata{}, Event{}, fmt.Errorf("goal has no tasks")
	}
	current := CurrentGoalTask(session.Goal)
	if current == nil {
		return Metadata{}, Event{}, fmt.Errorf("goal has no open task")
	}
	current.Status = GoalTaskDone
	current.Note = strings.TrimSpace(note)
	session.Goal.ContinuationCount = 0
	if session.Goal.ContinuationLimit <= 0 {
		session.Goal.ContinuationLimit = GoalMaxConsecutiveContinuations
	}
	if next := CurrentGoalTask(session.Goal); next != nil {
		next.Status = GoalTaskActive
	} else {
		session.Goal.Status = GoalStatusComplete
		session.Goal.StatusReason = "all tasks complete"
	}
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err = store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// BlockGoalTask marks the current task blocked with a reason and pauses the
// goal. Finished tasks stay finished; /goal resume retries the blocked task.
func (store *Store) BlockGoalTask(sessionID, reason string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	if strings.TrimSpace(reason) == "" {
		return Metadata{}, Event{}, fmt.Errorf("blocked tasks require a reason")
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	if len(session.Goal.Tasks) == 0 {
		return Metadata{}, Event{}, fmt.Errorf("goal has no tasks")
	}
	current := CurrentGoalTask(session.Goal)
	if current == nil {
		return Metadata{}, Event{}, fmt.Errorf("goal has no open task")
	}
	current.Status = GoalTaskBlocked
	current.Note = strings.TrimSpace(reason)
	session.Goal.Status = GoalStatusPaused
	session.Goal.StatusReason = strings.TrimSpace(reason)
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// ReactivateGoalTask returns a blocked current task to active so a resumed
// goal retries it. It is a no-op when the current task is not blocked.
func (store *Store) ReactivateGoalTask(sessionID string) (Metadata, Event, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	if session.Goal == nil {
		return Metadata{}, Event{}, fmt.Errorf("session has no goal")
	}
	current := CurrentGoalTask(session.Goal)
	if current == nil || normalizeGoalTaskStatus(current.Status) != GoalTaskBlocked {
		return session, Event{}, nil
	}
	current.Status = GoalTaskActive
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, Event{}, err
	}
	event, err := store.appendEventLocked(sessionID, AppendEventInput{
		Type:    EventGoalUpdated,
		Payload: session.Goal,
	})
	if err != nil {
		return Metadata{}, Event{}, err
	}
	loaded, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, Event{}, err
	}
	return loaded, event, nil
}

// SetGoalTaskBoundary records where the current task era starts (the
// first sequence of the next run) so reset mode can filter pre-task history.
// A zero seq clears the boundary. It writes metadata only, appending no
// event, so the head the boundary was derived from does not move.
func (store *Store) SetGoalTaskBoundary(sessionID string, seq int) (Metadata, error) {
	if !ValidSessionID(sessionID) {
		return Metadata{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Metadata{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Metadata{}, err
	}
	if session.Goal == nil {
		return Metadata{}, fmt.Errorf("session has no goal")
	}
	if seq < 0 {
		return Metadata{}, fmt.Errorf("goal task boundary cannot be negative")
	}
	session.Goal.TaskBoundary = seq
	session.Goal.UpdatedAt = store.timestamp()
	if err := store.writeMetadata(session); err != nil {
		return Metadata{}, err
	}
	return session, nil
}

func validGoalStatus(status GoalStatus) bool {
	switch status {
	case GoalStatusActive, GoalStatusPaused, GoalStatusBlocked, GoalStatusBudgetLimited, GoalStatusUsageLimited, GoalStatusComplete:
		return true
	default:
		return false
	}
}

func cloneGoal(goal *Goal) *Goal {
	if goal == nil {
		return nil
	}
	copy := *goal
	return &copy
}
