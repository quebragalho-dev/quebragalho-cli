package updatenotice

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
)

// Config wires the update notice to the outside world: who is asking, how the
// install was done, and where to look. Func fields default sanely when nil.
type Config struct {
	// CurrentVersion is the running qg version ("dev" for dev builds).
	CurrentVersion string
	// InstallMethod selects the release source: npm checks the npm registry,
	// anything else checks GitHub releases.
	InstallMethod update.InstallMethod
	// Env reads environment variables; nil reads the process environment.
	Env func(string) string
	// Now reports the current time; nil uses time.Now.
	Now func() time.Time
	// GitHubEndpoint overrides the GitHub latest-release API URL (tests, mirrors).
	GitHubEndpoint string
	// NpmEndpoint overrides the npm registry base URL (tests, mirrors).
	NpmEndpoint string
}

// ChangelogURL is the public CHANGELOG.md page for a release: the newest
// sections are on top, so one link covers every version the user skipped.
func ChangelogURL(version string) string {
	return "https://github.com/" + update.DefaultRepository + "/blob/v" + strings.TrimSpace(version) + "/CHANGELOG.md"
}

// Enabled reports whether update checking, the startup prompt, and the footer
// notice may run at all: never for dev builds or Homebrew installs (the
// formula is not published, so there is nothing to offer), and never with
// QG_NO_UPDATE_CHECK=1/true.
func Enabled(config Config) bool {
	if strings.TrimSpace(config.CurrentVersion) == "dev" {
		return false
	}
	if config.InstallMethod == update.InstallMethodHomebrew {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(lookupEnv(config.Env, "QG_NO_UPDATE_CHECK"))) {
	case "1", "true":
		return false
	}
	return true
}

// Due reports whether the stored check is stale: the first check always runs,
// afterwards at most once an hour. A LastCheck in the future (clock moved
// backward, or a state file from a skewed machine) is due too: checking again
// records a fresh LastCheck instead of waiting out the future timestamp.
func Due(config Config, state State) bool {
	elapsed := now(config.Now).Sub(state.LastCheck)
	if elapsed < 0 {
		return true
	}
	return elapsed >= time.Hour
}

// Update loads the state file, applies mutate, and saves it back atomically.
// Every writer re-reads the file at save time, so independent fields survive
// interleaved writers: a background refresh that finishes after the user
// pressed "skip" keeps the recorded skip (and vice versa) instead of blanking
// it with a stale copy. The last writer still wins on the same field.
func Update(path string, mutate func(*State)) error {
	state := Load(path)
	mutate(&state)
	return Save(path, state)
}

// Refresh checks for a new release and folds it into a copy of the state:
// success records LatestVersion; the check time is always recorded, even on
// error, so a dead network backs off instead of retrying every turn. Errors
// are swallowed — callers treat a silent state as "unknown", never fatal —
// and Refresh never panics on zero-value Config fields.
func Refresh(ctx context.Context, config Config, state State) State {
	if latest, ok := checkLatest(ctx, config); ok {
		state.LatestVersion = latest
	}
	state.LastCheck = now(config.Now)
	return state
}

// PromptVersion is the version the startup prompt and the footer notice may
// offer: the latest known release when it is newer than the running build and
// the user has not declined it. Anything else (unknown, current, older,
// declined, unparseable) stays silent.
func PromptVersion(config Config, state State) string {
	latest := strings.TrimSpace(state.LatestVersion)
	if latest == "" || latest == strings.TrimSpace(state.SkippedVersion) {
		return ""
	}
	newer, err := update.CompareVersions(latest, config.CurrentVersion)
	if err != nil || newer <= 0 {
		return ""
	}
	return latest
}

func checkLatest(ctx context.Context, config Config) (string, bool) {
	if config.InstallMethod == update.InstallMethodNpm {
		result, err := update.CheckNpm(ctx, update.Options{
			CurrentVersion: config.CurrentVersion,
			Endpoint:       strings.TrimSpace(config.NpmEndpoint),
		})
		if err != nil {
			return "", false
		}
		return result.LatestVersion, true
	}
	result, err := update.Check(ctx, update.Options{
		CurrentVersion: config.CurrentVersion,
		Endpoint:       strings.TrimSpace(config.GitHubEndpoint),
	})
	if err != nil {
		return "", false
	}
	return result.LatestVersion, true
}

func now(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now()
	}
	return clock()
}

func lookupEnv(env func(string) string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env(key)
}
