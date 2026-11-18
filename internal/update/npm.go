package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/envstr"
)

// DefaultNpmRegistry is the npm registry queried for the latest published
// @quebragalho/cli version. An install still under the legacy name is checked
// against the current name too: that is where its next version is. Overridden by Options.Endpoint in tests and
// by QG_UPDATE_NPM_ENDPOINT where the caller wires it.
const DefaultNpmRegistry = "https://registry.npmjs.org"

// npmLatestVersion is the subset of the npm registry `.../latest` document
// the check reads.
type npmLatestVersion struct {
	Version string `json:"version"`
}

// CheckNpm reports the latest published version of the qg npm package. It
// reuses Result, the semver comparison, and the timeout behavior of Check;
// asset verification does not apply (npm serves no GitHub archives).
func CheckNpm(ctx context.Context, options Options) (Result, error) {
	rawVersion := strings.TrimSpace(envstr.FirstNonEmpty(options.CurrentVersion, "0.0.0"))
	currentVersion, err := normalizeVersionTag(rawVersion)
	if err != nil {
		// Same convention as Check: dev builds compare as 0.0.0.
		currentVersion = "0.0.0"
	}
	base := strings.TrimSpace(envstr.FirstNonEmpty(options.Endpoint, DefaultNpmRegistry))
	trimmedBase := strings.TrimRight(base, "/")
	parsed, err := url.ParseRequestURI(trimmedBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Result{}, fmt.Errorf("invalid npm registry endpoint %q: use a base URL like %s", base, DefaultNpmRegistry)
	}
	if err := requireSecureURL(trimmedBase); err != nil {
		return Result{}, err
	}
	endpoint := trimmedBase + "/" + url.PathEscape(npmPackageName) + "/latest"
	if err := requireSecureURL(endpoint); err != nil {
		return Result{}, err
	}
	timeout := options.Timeout
	if timeout < 0 {
		return Result{}, fmt.Errorf("timeout must be non-negative")
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "qg/update")
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("npm registry check failed (%s)", response.Status)
	}
	var latest npmLatestVersion
	if err := json.NewDecoder(response.Body).Decode(&latest); err != nil {
		return Result{}, err
	}
	latestVersion, err := normalizeVersionTag(latest.Version)
	if err != nil {
		return Result{}, err
	}
	latestParts, err := parseSemverNormalized(latestVersion)
	if err != nil {
		return Result{}, err
	}
	currentParts, err := parseSemverNormalized(currentVersion)
	if err != nil {
		return Result{}, err
	}
	return Result{
		CurrentVersion:  currentVersion,
		LatestVersion:   latestVersion,
		UpdateAvailable: compareSemverParts(latestParts, currentParts) > 0,
	}, nil
}

// CompareVersions compares two version tags ("v" prefix and prerelease/build
// suffixes tolerated) as x.y.z triples, returning -1, 0, or +1. It backs
// callers that need Check's ordering without a network round trip.
func CompareVersions(left string, right string) (int, error) {
	leftNormalized, err := normalizeVersionTag(left)
	if err != nil {
		return 0, err
	}
	rightNormalized, err := normalizeVersionTag(right)
	if err != nil {
		return 0, err
	}
	leftParts, err := parseSemverNormalized(leftNormalized)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseSemverNormalized(rightNormalized)
	if err != nil {
		return 0, err
	}
	return compareSemverParts(leftParts, rightParts), nil
}
