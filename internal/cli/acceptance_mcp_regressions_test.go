package cli

// Contracts the re-audit of project trust found broken: a project allow-list
// only narrows the user's, and `qg mcp approve` never approves what nobody
// confirmed.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// When the project's enabled_tools shares no tool with the user's, the server
// exposes none of them, never all.
func TestAcceptanceMCPProjectAllowListNeverWidensTheUsers(t *testing.T) {
	acceptanceHermeticEnv(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	user := strings.TrimSuffix(acceptanceStdioServerJSON(t, marker, ""), "}") + `,"enabled_tools":["other"]}`
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},"u":`+user+`}}}`)
	repo := acceptanceRepo(t, `{"u":{"timeout":5000,"enabled_tools":["lookup"]}}`, true)
	acceptanceApproveProjectServer(t, repo, "u")

	if _, visible := acceptanceRegisterWorkspace(t, repo).Get("mcp_u_lookup"); visible {
		t.Fatal(`project "enabled_tools":["lookup"] exposed mcp_u_lookup, which the user's ["other"] does not allow`)
	}
}

// Without --yes, approval needs a terminal to confirm on. With no terminal it
// is refused and the server stays unapproved.
func TestAcceptanceMCPApproveWithoutTerminalOrYesIsRefused(t *testing.T) {
	acceptanceHermeticEnv(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	repo := acceptanceRepo(t, `{"proj":`+acceptanceStdioServerJSON(t, marker, "")+`}`, true)

	var stdout, stderr bytes.Buffer
	deps := appDeps{getwd: func() (string, error) { return repo, nil }, stdin: strings.NewReader("s\n")}
	if code := runWithDeps([]string{"mcp", "approve", "proj"}, &stdout, &stderr, deps); code == exitSuccess {
		t.Fatalf("qg mcp approve proj with no terminal and no --yes succeeded:\n%s%s", stdout.String(), stderr.String())
	}
	if _, visible := acceptanceRegisterWorkspace(t, repo).Get("mcp_proj_lookup"); visible {
		t.Fatal("the refused approval still let the project server's tools into the workspace")
	}
}
