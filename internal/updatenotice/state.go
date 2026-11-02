package updatenotice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// State is the persisted update-notice state: when the registry was last
// checked, the latest version seen, and the version the user declined.
type State struct {
	LastCheck      time.Time `json:"lastCheck"`
	LatestVersion  string    `json:"latestVersion"`
	SkippedVersion string    `json:"skippedVersion"`
}

// StatePath returns the state file path: <state>/qg/update.json, where
// <state> follows the same root rule as the session worktrees.
func StatePath(env func(string) string) (string, error) {
	root, err := stateDirRoot(runtime.GOOS, env)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "qg", "update.json"), nil
}

// stateDirRoot mirrors the state-directory rule internal/worktrees uses for
// its base dir (XDG_STATE_HOME, else ~/.local/state; on Windows LOCALAPPDATA,
// else USERPROFILE\AppData\Local). Duplicated here so the notice package does
// not depend on the worktree domain for a path rule.
func stateDirRoot(goos string, env func(string) string) (string, error) {
	get := func(key string) string {
		if env == nil {
			return strings.TrimSpace(os.Getenv(key))
		}
		return strings.TrimSpace(env(key))
	}
	if goos == "windows" {
		if localAppData := get("LOCALAPPDATA"); localAppData != "" {
			return localAppData, nil
		}
		if profile := get("USERPROFILE"); profile != "" {
			return filepath.Join(profile, "AppData", "Local"), nil
		}
		return "", fmt.Errorf("resolve Windows state directory: set LOCALAPPDATA or USERPROFILE")
	}
	if stateHome := get("XDG_STATE_HOME"); stateHome != "" {
		return stateHome, nil
	}
	home := get("HOME")
	if home == "" {
		home = get("USERPROFILE")
	}
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
	}
	return filepath.Join(home, ".local", "state"), nil
}

// Load reads the state file. A missing, unreadable, or corrupt file behaves
// like an empty state: the next check simply runs.
func Load(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}
	}
	return state
}

// Save writes the state atomically: the parent directory is created with
// owner-only permissions, the payload goes to a temp file in the same
// directory with 0600, and a rename publishes it (atomic on POSIX; on Windows
// os.Rename replaces the destination).
func Save(path string, state State) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "update-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	failed = false
	return nil
}
