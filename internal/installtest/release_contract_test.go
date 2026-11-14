package installtest

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The repo is public, so pull_request workflows must never reach a self-hosted
// runner: every job runs on a GitHub-hosted runner. Cross-platform binaries
// come from Go cross-compilation, not from per-OS runners; the only exception
// is the native macOS unit-test job, which needs a GitHub-hosted macOS
// runner (free for public repos).
var hostedRunnersByWorkflow = map[string][]string{
	"ci.yml":                {"ubuntu-latest", "macos-latest"},
	"release-artifacts.yml": {"ubuntu-latest"},
	"pr-auto-review.yml":    {"ubuntu-latest"},
	"qg-action-smoke.yml":   {"ubuntu-latest"},
	"sdk.yml":               {"ubuntu-latest"},
	"sdk-publish.yml":       {"ubuntu-latest"},
}

type runsOnWorkflow struct {
	Jobs map[string]struct {
		RunsOn any `yaml:"runs-on"`
	} `yaml:"jobs"`
}

func TestWorkflowsNeverRunOnSelfHostedRunners(t *testing.T) {
	for name, allowed := range hostedRunnersByWorkflow {
		rel := filepath.Join(".github", "workflows", name)
		var doc runsOnWorkflow
		if err := yaml.Unmarshal([]byte(readRepoText(t, rel)), &doc); err != nil {
			t.Fatalf("%s: yaml: %v", rel, err)
		}
		if len(doc.Jobs) == 0 {
			t.Fatalf("%s: no jobs parsed", rel)
		}
		for jobName, job := range doc.Jobs {
			label, ok := job.RunsOn.(string)
			if !ok || !slices.Contains(allowed, label) {
				t.Errorf("%s job %q: runs-on = %v, want one of %v (the repo is public, so no self-hosted runners)", rel, jobName, job.RunsOn, allowed)
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
