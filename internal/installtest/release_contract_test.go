package installtest

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// The org has no hosted-runner quota: every job in the CI and release workflows
// must run on the self-hosted macOS ARM64 runner. Cross-platform binaries come
// from Go cross-compilation on that runner, not from per-OS hosted runners.
var selfHostedWorkflows = []string{
	"ci.yml",
	"release-please.yml",
	"release-artifacts.yml",
}

type runsOnWorkflow struct {
	Jobs map[string]struct {
		RunsOn any `yaml:"runs-on"`
	} `yaml:"jobs"`
}

func TestReleaseWorkflowsRunOnSelfHostedOnly(t *testing.T) {
	for _, name := range selfHostedWorkflows {
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
			if !ok || label != "self-hosted" {
				t.Errorf("%s job %q: runs-on = %v, want exactly the string self-hosted", rel, jobName, job.RunsOn)
			}
		}
	}
}

// Package.json and the release-please manifest must carry the same valid
// semver, or the tag-vs-package.json guard in release-artifacts.yml rejects
// the release and installers build 404 download URLs.
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
	var manifest map[string]string
	if err := json.Unmarshal([]byte(readRepoText(t, ".release-please-manifest.json")), &manifest); err != nil {
		t.Fatalf("parse .release-please-manifest.json: %v", err)
	}
	if manifest["."] != pkg.Version {
		t.Fatalf("release-please manifest version = %q, want package.json version %q", manifest["."], pkg.Version)
	}
}
