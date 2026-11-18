package npmwrapper

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeRegistryNpm is an npm stand-in for scripts/npm/publish-packages.sh. It
// keeps the registry in a JSON file: `npm view <name>@<v> version` answers
// from the published versions, `npm view <name> dist-tags.latest` from the
// tags, and `npm publish` (run in a payload directory) records the version
// under its --tag, `latest` by default. Every call is logged.
const fakeRegistryNpm = `
const fs = require('fs');
const path = require('path');
const state = process.env.FAKE_REGISTRY;
const registry = fs.existsSync(state) ? JSON.parse(fs.readFileSync(state, 'utf8')) : { versions: [], tags: {} };
const args = process.argv.slice(2);
fs.appendFileSync(process.env.FAKE_NPM_LOG, args.join(' ') + '\n');
const save = () => fs.writeFileSync(state, JSON.stringify(registry));
if (args[0] === 'view') {
  const spec = args[1];
  if (args[2] === 'version') {
    const at = spec.lastIndexOf('@');
    const version = spec.slice(at + 1);
    if (registry.versions.includes(version)) { process.stdout.write(version + '\n'); process.exit(0); }
    process.exit(1);
  }
  if (args[2] === 'dist-tags.latest') {
    if (registry.afterWrapper && registry.lagLatestReads > 0) {
      registry.lagLatestReads -= 1;
      save();
      if (registry.staleLatest) { process.stdout.write(registry.staleLatest + '\n'); process.exit(0); }
      process.exit(1);
    }
    if (registry.tags.latest) { process.stdout.write(registry.tags.latest + '\n'); process.exit(0); }
    process.exit(1);
  }
  process.exit(1);
}
if (args[0] === 'publish') {
  const pkg = JSON.parse(fs.readFileSync(path.join(process.cwd(), 'package.json'), 'utf8'));
  const tagIndex = args.indexOf('--tag');
  const tag = tagIndex === -1 ? 'latest' : args[tagIndex + 1];
  if (tag === 'latest') {
    registry.afterWrapper = true;
  }
  registry.versions.push(pkg.version);
  registry.tags[tag] = pkg.version;
  save();
  process.exit(0);
}
process.exit(1);
`

type publishFixture struct {
	npmDir   string
	registry string
	npmLog   string
}

func newPublishFixture(t *testing.T, name string, version string, platforms []string) publishFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("publish-packages.sh is a bash script run on the Linux release runner")
	}
	requireNode(t)
	requireCommand(t, "bash")
	root := t.TempDir()
	f := publishFixture{
		npmDir:   filepath.Join(root, "npm"),
		registry: filepath.Join(root, "registry.json"),
		npmLog:   filepath.Join(root, "npm-calls.log"),
	}
	aliases := map[string]string{}
	for _, platform := range platforms {
		aliases[name+"-"+platform] = "npm:" + name + "@" + version + "-" + platform
		writeFixtureFile(t, filepath.Join(f.npmDir, "platforms", "qg-"+platform, "package.json"),
			`{"name":"`+name+`","version":"`+version+"-"+platform+`"}`, 0o644)
	}
	wrapper, err := json.Marshal(map[string]any{"name": name, "version": version, "optionalDependencies": aliases})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(f.npmDir, "wrapper", "package.json"), string(wrapper), 0o644)
	shimDir := filepath.Join(root, "shims")
	writeFixtureFile(t, filepath.Join(root, "fake-npm.js"), fakeRegistryNpm, 0o644)
	writeFixtureFile(t, filepath.Join(shimDir, "npm"), "#!/bin/sh\nexec node "+shellQuote(filepath.Join(root, "fake-npm.js"))+" \"$@\"\n", 0o755)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_REGISTRY", f.registry)
	t.Setenv("FAKE_NPM_LOG", f.npmLog)
	t.Setenv("QG_NPM_PUBLISH_RETRY_DELAY", "0")
	return f
}

func (f publishFixture) run(t *testing.T) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	script := filepath.Join(repoRoot(t), "scripts", "npm", "publish-packages.sh")
	output, err := exec.CommandContext(ctx, "bash", script, f.npmDir).CombinedOutput()
	return string(output), err
}

func (f publishFixture) publishes(t *testing.T) []string {
	t.Helper()
	var got []string
	for _, line := range readLines(t, f.npmLog) {
		if strings.HasPrefix(line, "publish") {
			got = append(got, line)
		}
	}
	return got
}

// TestPublishPackagesOrderAndTags pins the publishing rules for whatever name
// the payloads carry: platform payloads first under the platform tag, the
// wrapper last under latest, and latest ends on the wrapper version.
func TestPublishPackagesOrderAndTags(t *testing.T) {
	f := newPublishFixture(t, "@quebragalho-dev/cli", "0.0.16", []string{"linux-x64", "darwin-arm64"})
	output, err := f.run(t)
	if err != nil {
		t.Fatalf("publish-packages.sh failed: %v\n%s", err, output)
	}
	assertLines(t, "npm publishes", f.publishes(t), []string{
		"publish --access public --ignore-scripts --tag platform",
		"publish --access public --ignore-scripts --tag platform",
		"publish --access public --ignore-scripts",
	})
	raw, err := os.ReadFile(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Versions []string          `json:"versions"`
		Tags     map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.Tags["latest"] != "0.0.16" || len(registry.Versions) != 3 {
		t.Fatalf("registry = %+v, want three versions and latest 0.0.16", registry)
	}
	if !strings.Contains(output, "dist-tag latest of @quebragalho-dev/cli is 0.0.16.") {
		t.Fatalf("output = %s, want the latest confirmation for the payloads' own name", output)
	}
}

// TestPublishPackagesRerunSkipsWhatWentOut: a job re-run after a failure in
// the middle publishes only what is missing.
func TestPublishPackagesRerunSkipsWhatWentOut(t *testing.T) {
	f := newPublishFixture(t, "@quebragalho/cli", "0.0.16", []string{"linux-x64", "darwin-arm64"})
	writeFixtureFile(t, f.registry, `{"versions":["0.0.16-linux-x64"],"tags":{"platform":"0.0.16-linux-x64","latest":"0.0.15"}}`, 0o644)
	output, err := f.run(t)
	if err != nil {
		t.Fatalf("publish-packages.sh failed: %v\n%s", err, output)
	}
	assertLines(t, "npm publishes", f.publishes(t), []string{
		"publish --access public --ignore-scripts --tag platform",
		"publish --access public --ignore-scripts",
	})
	if !strings.Contains(output, "@quebragalho/cli@0.0.16-linux-x64 is already on the registry") {
		t.Fatalf("output = %s, want the skipped version named", output)
	}
}

// TestPublishPackagesRefusesMismatchedPayloads: a wrapper whose aliases do
// not match the payloads beside it would install aliases that 404, so the
// script stops before publishing anything.
func TestPublishPackagesRefusesMismatchedPayloads(t *testing.T) {
	f := newPublishFixture(t, "@quebragalho/cli", "0.0.16", []string{"linux-x64"})
	writeFixtureFile(t, filepath.Join(f.npmDir, "platforms", "qg-linux-x64", "package.json"), `{"name":"@quebragalho-dev/cli","version":"0.0.16-linux-x64"}`, 0o644)
	output, err := f.run(t)
	if err == nil {
		t.Fatalf("publish-packages.sh succeeded with a payload of another name:\n%s", output)
	}
	if !strings.Contains(output, "is @quebragalho-dev/cli, not @quebragalho/cli") {
		t.Fatalf("output = %s, want the mismatch named", output)
	}
	if _, statErr := os.Stat(f.npmLog); statErr == nil && len(f.publishes(t)) != 0 {
		t.Fatalf("published %v before refusing", f.publishes(t))
	}
}

func installSleepRecorder(t *testing.T) string {
	t.Helper()
	shimDir := t.TempDir()
	sleepLog := filepath.Join(shimDir, "sleep.log")
	writeFixtureFile(t, filepath.Join(shimDir, "sleep"),
		"#!/bin/sh\nprintf '%s\\n' \"$1\" >> "+shellQuote(sleepLog)+"\n", 0o755)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return sleepLog
}

// TestPublishPackagesWaitsUpToTenMinutesForLatestPropagation covers npm
// registry lag where dist-tags.latest takes several minutes (>150 s) to move
// onto the newly published wrapper version.
func TestPublishPackagesWaitsUpToTenMinutesForLatestPropagation(t *testing.T) {
	t.Run("succeeds when latest catches up after several minutes", func(t *testing.T) {
		f := newPublishFixture(t, "@quebragalho/cli", "0.0.21", []string{"linux-x64"})
		sleepLog := installSleepRecorder(t)
		t.Setenv("QG_NPM_PUBLISH_RETRY_DELAY", "")
		// 18 lagging lookups at the default 20 s interval = 360 s (6 min), well
		// past the ~150 s lag seen on 0.0.21 and the old 12-lookup cap.
		writeFixtureFile(t, f.registry,
			`{"versions":[],"tags":{"latest":"0.0.20"},"staleLatest":"0.0.20","lagLatestReads":18}`, 0o644)

		output, err := f.run(t)
		if err != nil {
			t.Fatalf("publish-packages.sh gave up before latest caught up: %v\n%s", err, output)
		}
		if !strings.Contains(output, "dist-tag latest of @quebragalho/cli is 0.0.21.") {
			t.Fatalf("output = %s, want latest 0.0.21 confirmation", output)
		}
		sleeps := readLines(t, sleepLog)
		if len(sleeps) != 18 {
			t.Fatalf("sleep count = %d (%v), want 18", len(sleeps), sleeps)
		}
		for _, s := range sleeps {
			if s != "20" {
				t.Fatalf("default retry delay = %q, want 20s", s)
			}
		}
	})

	t.Run("fails after exhausting the full 10-minute window", func(t *testing.T) {
		f := newPublishFixture(t, "@quebragalho/cli", "0.0.21", []string{"linux-x64"})
		sleepLog := installSleepRecorder(t)
		t.Setenv("QG_NPM_PUBLISH_RETRY_DELAY", "")
		writeFixtureFile(t, f.registry,
			`{"versions":[],"tags":{"latest":"0.0.20"},"staleLatest":"0.0.20","lagLatestReads":999}`, 0o644)

		output, err := f.run(t)
		if err == nil {
			t.Fatalf("publish-packages.sh succeeded even though latest never moved:\n%s", output)
		}
		if !strings.Contains(output, "dist-tag latest of @quebragalho/cli is 0.0.20, expected 0.0.21") {
			t.Fatalf("output = %s, want expected-version failure", output)
		}
		sleeps := readLines(t, sleepLog)
		// 30 sleeps of 20 s between 31 lookups = 600 s (10 minutes).
		if len(sleeps) != 30 {
			t.Fatalf("sleep count = %d (%v), want 30 intervals of 20s (600s total)", len(sleeps), sleeps)
		}
	})
}
