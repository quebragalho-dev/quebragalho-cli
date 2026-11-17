package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goalTestProviders() string {
	return `"activeProvider": "user",
		"providers": [{"name": "user", "provider": "openai", "api_key": "sk-user", "model_id": "gpt-user"}]`
}

func TestGoalConfigDefaults(t *testing.T) {
	path := writeConfig(t, `{`+goalTestProviders()+`}`)
	resolved, err := Resolve(ResolveOptions{UserConfigPath: path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolved.Goal.AutoSplitEnabled() {
		t.Fatal("autoSplit should default to on")
	}
	if resolved.Goal.BetweenTasksMode() != GoalBetweenTasksCompact {
		t.Fatalf("betweenTasks mode = %q, want compact", resolved.Goal.BetweenTasksMode())
	}
}

func TestGoalConfigInvalidModeIsRejected(t *testing.T) {
	path := writeConfig(t, `{`+goalTestProviders()+`, "goal": {"betweenTasks": "turbo"}}`)
	if _, err := Resolve(ResolveOptions{UserConfigPath: path, Env: map[string]string{}}); err == nil ||
		!strings.Contains(err.Error(), "goal.betweenTasks") {
		t.Fatalf("invalid mode error = %v", err)
	}
	path = writeConfig(t, `{`+goalTestProviders()+`}`)
	env := map[string]string{GoalBetweenTasksEnv: "turbo"}
	if _, err := Resolve(ResolveOptions{UserConfigPath: path, Env: env}); err == nil ||
		!strings.Contains(err.Error(), "goal.betweenTasks") {
		t.Fatalf("invalid env mode error = %v", err)
	}
}

func TestGoalConfigEnvBeatsFile(t *testing.T) {
	path := writeConfig(t, `{`+goalTestProviders()+`, "goal": {"autoSplit": true, "betweenTasks": "keep"}}`)
	env := map[string]string{
		GoalAutoSplitEnv:    "false",
		GoalBetweenTasksEnv: "reset",
	}
	resolved, err := Resolve(ResolveOptions{UserConfigPath: path, Env: env})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Goal.AutoSplitEnabled() {
		t.Fatal("env QG_GOAL_AUTO_SPLIT=false should disable autoSplit")
	}
	if resolved.Goal.BetweenTasksMode() != GoalBetweenTasksReset {
		t.Fatalf("env betweenTasks mode = %q, want reset", resolved.Goal.BetweenTasksMode())
	}
}

func TestSetGoalAutoSplitPersistsAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	off := false
	cfg, err := SetGoalAutoSplit(path, &off)
	if err != nil {
		t.Fatalf("SetGoalAutoSplit() error = %v", err)
	}
	if cfg.Goal.AutoSplit == nil || *cfg.Goal.AutoSplit {
		t.Fatalf("persisted autoSplit = %#v, want false", cfg.Goal.AutoSplit)
	}
	cfg, err = SetGoalAutoSplit(path, nil)
	if err != nil {
		t.Fatalf("SetGoalAutoSplit(nil) error = %v", err)
	}
	if cfg.Goal.AutoSplit != nil {
		t.Fatalf("cleared autoSplit = %#v, want nil", cfg.Goal.AutoSplit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "autoSplit") {
		t.Fatalf("cleared autoSplit should be omitted from JSON: %s", data)
	}
}

func TestSetGoalBetweenTasksPersistsValidatesAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	if _, err := SetGoalBetweenTasks(path, "turbo"); err == nil ||
		!strings.Contains(err.Error(), "goal.betweenTasks") {
		t.Fatalf("invalid mode error = %v", err)
	}
	cfg, err := SetGoalBetweenTasks(path, "Reset")
	if err != nil {
		t.Fatalf("SetGoalBetweenTasks() error = %v", err)
	}
	if cfg.Goal.BetweenTasks != GoalBetweenTasksReset {
		t.Fatalf("persisted betweenTasks = %q, want reset", cfg.Goal.BetweenTasks)
	}
	cfg, err = SetGoalBetweenTasks(path, "")
	if err != nil {
		t.Fatalf("SetGoalBetweenTasks(\"\") error = %v", err)
	}
	if cfg.Goal.BetweenTasks != "" {
		t.Fatalf("cleared betweenTasks = %q, want empty", cfg.Goal.BetweenTasks)
	}
}
