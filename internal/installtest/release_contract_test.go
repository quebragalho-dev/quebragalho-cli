package installtest

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The repo is public, so pull_request workflows must never reach a self-hosted
// runner: every job runs on a GitHub-hosted Ubuntu runner. Cross-platform
// binaries come from Go cross-compilation, not from per-OS runners.
var hostedWorkflows = []string{
	"ci.yml",
	"release-artifacts.yml",
	"pr-auto-review.yml",
	"qg-action-smoke.yml",
}

type runsOnWorkflow struct {
	Jobs map[string]struct {
		RunsOn any `yaml:"runs-on"`
	} `yaml:"jobs"`
}

func TestWorkflowsNeverRunOnSelfHostedRunners(t *testing.T) {
	for _, name := range hostedWorkflows {
		rel := filepath.Join(".github", "workflows", name)
		var doc runsOnWorkflow
		if err := yaml.Unmarshal([]byte(readRepoText(t, rel)), &doc); err != nil {
			t.Fatalf("%s: yaml: %v", rel, err)
		}
		if len(doc.Jobs) == 0 {
			t.Fatalf("%s: no jobs parsed", rel)
		}
		for jobName, job := range doc.Jobs {
			if label, ok := job.RunsOn.(string); !ok || label != "ubuntu-latest" {
				t.Errorf("%s job %q: runs-on = %v, want ubuntu-latest (the repo is public, so no self-hosted runners)", rel, jobName, job.RunsOn)
			}
		}
	}
}

// Package.json must carry a valid semver and CHANGELOG.md must already have
// its section, or the tag guard in release-artifacts.yml rejects the release.
var releaseVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func TestReleaseVersionIsFirstCut(t *testing.T) {
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(readRepoText(t, "package.json")), &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if !releaseVersionPattern.MatchString(pkg.Version) {
		t.Fatalf("package.json version = %q, want valid semver X.Y.Z", pkg.Version)
	}
	changelog := readRepoText(t, "CHANGELOG.md")
	heading := "## " + pkg.Version + " — "
	hasSection := false
	for _, line := range strings.Split(changelog, "\n") {
		if strings.HasPrefix(line, heading) {
			hasSection = true
			break
		}
	}
	if !hasSection {
		t.Fatalf("CHANGELOG.md has no section for package.json version %q (want a %q line)", pkg.Version, heading)
	}
}
