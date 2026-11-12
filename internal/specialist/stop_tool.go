package specialist

import (
	"context"
	"fmt"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/background"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type StopTool struct {
	manager     *background.Manager
	managerFunc BackgroundManagerFunc
}

type stopParameters struct {
	TaskID string
}

func newStopToolWithManagerFunc(managerFunc BackgroundManagerFunc) *StopTool {
	return &StopTool{managerFunc: managerFunc}
}

func (tool *StopTool) Name() string {
	return "TaskStop"
}

func (tool *StopTool) Description() string {
	return "Stop a running background QG specialist task."
}

func (tool *StopTool) Parameters() tools.Schema {
	return tools.Schema{
		Type: "object",
		Properties: map[string]tools.PropertySchema{
			"task_id": {
				Type:        "string",
				Description: "Background task id returned by Task.",
			},
		},
		Required:             []string{"task_id"},
		AdditionalProperties: false,
	}
}

func (tool *StopTool) Safety() tools.Safety {
	return tools.Safety{
		SideEffect:      tools.SideEffectShell,
		Permission:      tools.PermissionPrompt,
		Reason:          "Terminates a background specialist process.",
		AdvertiseInAuto: true,
	}
}

func (tool *StopTool) Run(ctx context.Context, args map[string]any) tools.Result {
	params, err := parseStopParameters(args)
	if err != nil {
		return taskError(err)
	}
	if err := tool.KillTask(params.TaskID); err != nil {
		return taskError(err)
	}
	return tools.Result{
		Status: tools.StatusOK,
		Output: fmt.Sprintf("task_id: %s\nstatus: killed", params.TaskID),
		Meta: map[string]string{
			"task_id": params.TaskID,
			"status":  string(background.StatusKilled),
		},
	}
}

// KillTask stops a running background specialist task through the same manager
// the TaskStop tool uses, so the TUI panel can stop a task without
// round-tripping a tool call.
func (tool *StopTool) KillTask(taskID string) error {
	manager, err := tool.backgroundManager()
	if err != nil {
		return err
	}
	return manager.Kill(taskID)
}

// BackgroundTask reports a background task's latest known state, so surfaces
// outside the agent loop can follow a running task without polling the tool.
func (tool *StopTool) BackgroundTask(taskID string) (background.Task, bool) {
	manager, err := tool.backgroundManager()
	if err != nil {
		return background.Task{}, false
	}
	return manager.Get(taskID)
}

func (tool *StopTool) backgroundManager() (*background.Manager, error) {
	if tool.manager != nil {
		return tool.manager, nil
	}
	if tool.managerFunc != nil {
		return tool.managerFunc()
	}
	return nil, fmt.Errorf("background manager is not configured")
}

func parseStopParameters(args map[string]any) (stopParameters, error) {
	taskID, err := optionalTaskString(args, "task_id")
	if err != nil {
		return stopParameters{}, err
	}
	params := stopParameters{TaskID: strings.TrimSpace(taskID)}
	if params.TaskID == "" {
		return stopParameters{}, fmt.Errorf("task stop requires task_id")
	}
	return params, nil
}
