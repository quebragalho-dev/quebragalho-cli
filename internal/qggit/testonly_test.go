package qggit

import (
	"context"
	"strings"
)

// HasUpstream reports whether branch has a published upstream tracking the same
// branch name on a remote (the relationship `git push -u <remote> <branch>`
// records). Test-only: no production path consults it.
//
// An inherited upstream to a different branch (branch.autoSetupMerge=inherit
// copies origin/main onto a new user/slug) is not publication state and reports
// false. Any failure to resolve the upstream (including "no upstream
// configured") also reports false so the caller keeps requiring the lease.
func HasUpstream(ctx context.Context, cwd, branch string, runGit Runner) (bool, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, nil
	}
	ref := UpstreamRef(ctx, cwd, branch, runGit)
	if ref == "" {
		return false, nil
	}
	// rev-parse --abbrev-ref prints "<remote>/<branch>". Both remote names
	// (e.g. team/upstream) and branch names (e.g. user/slug) may contain
	// slashes, so verify that the upstream ref ends with "/" + branch and
	// has a non-empty remote prefix.
	suffix := "/" + branch
	if strings.HasSuffix(ref, suffix) && len(ref) > len(suffix) {
		return true, nil
	}
	return false, nil
}
