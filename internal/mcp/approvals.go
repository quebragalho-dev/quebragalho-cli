package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectApproval is one remembered user approval of a project-layer MCP
// server: which project, which server, and the exact target (identity) that
// was approved. Changing the target changes the identity and requires a
// fresh approval, so a repo cannot swap a vetted command for another one.
type ProjectApproval struct {
	ProjectRoot string
	ServerName  string
	Identity    string
}

// ProjectApprovals is the per-user store of project MCP approvals. It lives
// in the user state dir (XDG_STATE_HOME, else ~/.local/state), next to other
// user state, never in the repo.
type ProjectApprovals struct {
	path     string
	projects map[string]map[string]string
}

// OpenProjectApprovals loads the user's project approvals. A missing file is
// not an error: it just means nothing was approved yet.
func OpenProjectApprovals() (*ProjectApprovals, error) {
	path, err := projectApprovalsPath()
	if err != nil {
		return nil, err
	}
	store := &ProjectApprovals{path: path, projects: make(map[string]map[string]string)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return store, nil
	}
	var raw struct {
		Projects map[string]map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Projects != nil {
		store.projects = raw.Projects
	}
	return store, nil
}

func projectApprovalsPath() (string, error) {
	root, err := userStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "qg", "mcp_approvals.json"), nil
}

// userStateDir mirrors the XDG state rule used elsewhere: XDG_STATE_HOME,
// else ~/.local/state (LOCALAPPDATA on Windows).
func userStateDir() (string, error) {
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return localAppData, nil
		}
		if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
			return filepath.Join(profile, "AppData", "Local"), nil
		}
		return "", fmt.Errorf("resolve user state directory: set LOCALAPPDATA or USERPROFILE")
	}
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
		return stateHome, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve user state directory: set XDG_STATE_HOME or HOME")
	}
	return filepath.Join(home, ".local", "state"), nil
}

// Approve records the approval of one server target in one project and
// persists the store atomically with owner-only permissions.
func (store *ProjectApprovals) Approve(approval ProjectApproval) error {
	if store == nil {
		return fmt.Errorf("mcp project approvals store is required")
	}
	if store.projects == nil {
		store.projects = make(map[string]map[string]string)
	}
	servers, ok := store.projects[approval.ProjectRoot]
	if !ok || servers == nil {
		servers = make(map[string]string)
		store.projects[approval.ProjectRoot] = servers
	}
	servers[approval.ServerName] = approval.Identity
	return store.save()
}

// IsApproved reports whether this exact server target was approved in this
// project. Empty identities never match.
func (store *ProjectApprovals) IsApproved(approval ProjectApproval) bool {
	if store == nil || approval.Identity == "" {
		return false
	}
	servers, ok := store.projects[approval.ProjectRoot]
	if !ok {
		return false
	}
	approved, ok := servers[approval.ServerName]
	return ok && approved == approval.Identity
}

func (store *ProjectApprovals) save() error {
	data, err := json.MarshalIndent(struct {
		Projects map[string]map[string]string `json:"projects"`
	}{Projects: store.projects}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	staging, err := os.CreateTemp(filepath.Dir(store.path), "mcp_approvals-*.json")
	if err != nil {
		return err
	}
	stagingName := staging.Name()
	defer func() { _ = os.Remove(stagingName) }()
	if _, err := staging.Write(append(data, '\n')); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Close(); err != nil {
		return err
	}
	if err := os.Chmod(stagingName, 0o600); err != nil {
		return err
	}
	return os.Rename(stagingName, store.path)
}

// ApprovalPendingError is the skipped-server reason for a project server the
// user has not approved yet.
type ApprovalPendingError struct {
	Server  string
	Project string
}

func (err *ApprovalPendingError) Error() string {
	where := ""
	if strings.TrimSpace(err.Project) != "" {
		where = " do projeto " + err.Project
	}
	return "servidor MCP " + err.Server + where +
		" aguarda aprovação (rode `qg mcp approve " + err.Server + "`)"
}

// FilterUnapproved drops project-layer servers the user has not approved for
// this exact target, reporting each as skipped. User and default servers pass
// through untouched. A nil store approves nothing: project servers stay out
// until the user approves them (fail closed).
func FilterUnapproved(servers []Server, projectRoot string, approvals *ProjectApprovals) ([]Server, []SkippedServer) {
	kept := make([]Server, 0, len(servers))
	var skipped []SkippedServer
	for _, server := range servers {
		if !server.ProjectConfigured {
			kept = append(kept, server)
			continue
		}
		if approvals != nil && approvals.IsApproved(ProjectApproval{ProjectRoot: projectRoot, ServerName: server.Name, Identity: server.Identity}) {
			kept = append(kept, server)
			continue
		}
		skipped = append(skipped, SkippedServer{
			Name: server.Name,
			Err:  &ApprovalPendingError{Server: server.Name, Project: projectRoot},
		})
	}
	return kept, skipped
}
