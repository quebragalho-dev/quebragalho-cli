package cli

// CLI acceptance tests that record project approvals through
// mcp.OpenProjectApprovals.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptanceApproveProjectServer(t *testing.T, repo string, name string) {
	t.Helper()
	resolved, err := defaultAppDeps().resolveMCPConfig(repo, false)
	if err != nil {
		t.Fatalf("resolveMCPConfig: %v", err)
	}
	servers, err := mcp.NormalizeConfig(resolved)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	identity := ""
	for _, server := range servers {
		if server.Name == name {
			identity = server.Identity
		}
	}
	if identity == "" {
		t.Fatalf("project server %q not resolved", name)
	}
	approvals, err := mcp.OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals: %v", err)
	}
	if err := approvals.Approve(mcp.ProjectApproval{ProjectRoot: repo, ServerName: name, Identity: identity}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// Once approved, a trusted project's server runs at every spawn site, and its
// ${VAR} is expanded from the qg environment (allowed only for trusted
// projects). Guards against a gate that blocks project servers forever on one
// path.
func TestAcceptanceMCPApprovedProjectServerRunsWithExpandedVars(t *testing.T) {
	sites := []struct {
		name string
		run  func(t *testing.T, repo string) *tools.Registry
	}{
		{"workspace registration (exec, acp)", acceptanceRegisterWorkspace},
		{"interactive start", acceptanceStartInteractiveSession},
		{"qg mcp check proj", func(t *testing.T, repo string) *tools.Registry {
			stdout, stderr, code := acceptanceRunCLI(t, repo, "mcp", "check", "proj")
			if code != exitSuccess {
				t.Fatalf("mcp check of an approved project server failed (exit %d):\n%s%s", code, stdout, stderr)
			}
			return nil
		}},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			t.Setenv("QG_ACCEPT_PROJECT_TOKEN", "trusted-project-value")
			marker := filepath.Join(t.TempDir(), "spawned")
			repo := acceptanceRepo(t, `{"proj":`+acceptanceStdioServerJSON(t, marker, `"ACCEPT_TOKEN":"${QG_ACCEPT_PROJECT_TOKEN}"`)+`}`, true)
			acceptanceApproveProjectServer(t, repo, "proj")

			registry := site.run(t, repo)
			if registry != nil {
				if _, ok := registry.Get("mcp_proj_lookup"); !ok {
					t.Fatalf("%s: approved project server's tools are missing", site.name)
				}
			}
			started, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("%s: approved project server never started: %v", site.name, err)
			}
			if !strings.Contains(string(started), "token=trusted-project-value") {
				t.Fatalf("%s: trusted project ${VAR} not expanded; server saw %q", site.name, strings.TrimSpace(string(started)))
			}
		})
	}
}
