package sessions

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Goal task states. A missing/empty state on a task loaded from disk is
// treated as pending so goals written before tasks existed keep working.
type GoalTaskStatus string

const (
	GoalTaskPending GoalTaskStatus = "pending"
	GoalTaskActive  GoalTaskStatus = "active"
	GoalTaskDone    GoalTaskStatus = "done"
	GoalTaskBlocked GoalTaskStatus = "blocked"
)

// MinGoalTasks/MaxGoalTasks bound automatic splits: fewer than two tasks is
// not a split, and more than twelve stops being an actionable checklist.
const (
	MinGoalTasks = 2
	MaxGoalTasks = 12
)

// GoalTask is one checklist item of a goal. Tasks is nil for legacy goals
// created before task support existed; those keep the single-objective
// behavior unchanged.
type GoalTask struct {
	ID     string         `json:"id"`
	Title  string         `json:"title"`
	Status GoalTaskStatus `json:"status"`
	Note   string         `json:"note,omitempty"`
}

var goalTaskItemPattern = regexp.MustCompile(`^(?:\d+[.)]|[-*•+])\s+\S`)

// ParseGoalText splits a goal body into an objective and a task list. Every
// numbered (`1.`, `2)`) or bulleted (`-`, `*`) line becomes one task; the
// first line that is not an item is the objective. A checkbox marker
// (`- [ ]`, `- [x]`) is stripped, and a checked item starts done. Without
// items the trimmed text is a plain single-sentence objective; without a
// non-item line the objective summarizes the task titles.
func ParseGoalText(input string) (string, []GoalTask) {
	var titles []string
	var done []bool
	var objective string
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		title, checked, ok := parseGoalTaskLine(trimmed)
		if !ok {
			if objective == "" {
				objective = trimmed
			}
			continue
		}
		titles = append(titles, title)
		done = append(done, checked)
	}
	if len(titles) == 0 {
		return strings.TrimSpace(input), nil
	}
	if objective == "" {
		objective = summarizeGoalTaskTitles(titles)
	}
	tasks := make([]GoalTask, len(titles))
	for i, title := range titles {
		status := GoalTaskPending
		if done[i] {
			status = GoalTaskDone
		}
		tasks[i] = GoalTask{
			ID:     fmt.Sprintf("t%d", i+1),
			Title:  title,
			Status: status,
		}
	}
	ensureActiveGoalTask(tasks)
	return objective, tasks
}

func parseGoalTaskLine(line string) (string, bool, bool) {
	if !goalTaskItemPattern.MatchString(line) {
		return "", false, false
	}
	idx := strings.IndexFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if idx < 0 {
		return "", false, false
	}
	title := strings.TrimSpace(line[idx+1:])
	if title == "" {
		return "", false, false
	}
	checked := false
	if rest, ok := strings.CutPrefix(title, "[x]"); ok {
		checked = true
		title = strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(title, "[X]"); ok {
		checked = true
		title = strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(title, "[ ]"); ok {
		title = strings.TrimSpace(rest)
	}
	if title == "" {
		return "", false, false
	}
	return title, checked, true
}

func summarizeGoalTaskTitles(titles []string) string {
	summary := strings.TrimSpace(strings.Join(titles, "; "))
	if utf8.RuneCountInString(summary) > GoalObjectiveMaxLength {
		runes := []rune(summary)
		summary = strings.TrimSpace(string(runes[:GoalObjectiveMaxLength]))
	}
	return summary
}

// normalizeGoalTaskStatus maps legacy/empty states to pending.
func normalizeGoalTaskStatus(status GoalTaskStatus) GoalTaskStatus {
	switch GoalTaskStatus(strings.ToLower(strings.TrimSpace(string(status)))) {
	case GoalTaskActive:
		return GoalTaskActive
	case GoalTaskDone:
		return GoalTaskDone
	case GoalTaskBlocked:
		return GoalTaskBlocked
	default:
		return GoalTaskPending
	}
}

// ensureActiveGoalTask keeps the at-most-one-active invariant: when no task
// is active, the first pending task becomes active.
func ensureActiveGoalTask(tasks []GoalTask) {
	for i := range tasks {
		tasks[i].Status = normalizeGoalTaskStatus(tasks[i].Status)
	}
	active := -1
	for i := range tasks {
		if tasks[i].Status == GoalTaskActive {
			if active >= 0 {
				tasks[i].Status = GoalTaskPending
				continue
			}
			active = i
		}
	}
	if active < 0 {
		for i := range tasks {
			if tasks[i].Status == GoalTaskPending {
				tasks[i].Status = GoalTaskActive
				return
			}
		}
	}
}

// CurrentGoalTask returns the task the model should work on: the active one,
// falling back to the first pending or blocked task when none is marked
// active. It returns nil for taskless (legacy) goals and finished goals.
func CurrentGoalTask(goal *Goal) *GoalTask {
	if goal == nil {
		return nil
	}
	for i := range goal.Tasks {
		if normalizeGoalTaskStatus(goal.Tasks[i].Status) == GoalTaskActive {
			return &goal.Tasks[i]
		}
	}
	for i := range goal.Tasks {
		switch normalizeGoalTaskStatus(goal.Tasks[i].Status) {
		case GoalTaskPending, GoalTaskBlocked:
			return &goal.Tasks[i]
		}
	}
	return nil
}

// GoalTaskProgress counts done tasks over the total for display ("3/7").
func GoalTaskProgress(goal *Goal) (done int, total int) {
	if goal == nil {
		return 0, 0
	}
	total = len(goal.Tasks)
	for _, task := range goal.Tasks {
		if normalizeGoalTaskStatus(task.Status) == GoalTaskDone {
			done++
		}
	}
	return done, total
}

// GoalTasksPending reports whether any task still needs work.
func GoalTasksPending(goal *Goal) bool {
	if goal == nil {
		return false
	}
	for _, task := range goal.Tasks {
		switch normalizeGoalTaskStatus(task.Status) {
		case GoalTaskPending, GoalTaskActive, GoalTaskBlocked:
			return true
		}
	}
	return false
}

// validateGoalTaskTitles cleans split titles: trims, drops empties, and
// enforces the 2-12 split bounds.
func validateGoalTaskTitles(titles []string) ([]string, error) {
	cleaned := make([]string, 0, len(titles))
	for _, title := range titles {
		if trimmed := strings.TrimSpace(title); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) < MinGoalTasks {
		return nil, fmt.Errorf("goal needs at least %d tasks, got %d", MinGoalTasks, len(cleaned))
	}
	if len(cleaned) > MaxGoalTasks {
		return nil, fmt.Errorf("goal accepts at most %d tasks, got %d", MaxGoalTasks, len(cleaned))
	}
	return cleaned, nil
}
