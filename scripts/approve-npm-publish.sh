#!/usr/bin/env bash
# Approve the npm-publish deployment gate of a release from the terminal.
#
#   scripts/approve-npm-publish.sh v0.0.10
#
# The gate stays on: this is the same "Review deployments -> Approve and deploy"
# click, done through the API with the caller's own gh login, so only a required
# reviewer can run it. Set QG_REPO=owner/name to target a fork.
set -euo pipefail

repo="${QG_REPO:-quebragalho-dev/quebragalho-cli}"
tag="${1:?usage: $0 vX.Y.Z}"

run="$(gh run list --repo "$repo" --workflow release-artifacts.yml --branch "$tag" \
  --json databaseId,status -q 'map(select(.status=="waiting"))[0].databaseId // empty')"
if [ -z "$run" ]; then
  echo "no release-artifacts run for $tag is waiting for approval (still packaging, or already decided)." >&2
  exit 1
fi

env_id="$(gh api "repos/$repo/actions/runs/$run/pending_deployments" \
  -q '.[] | select(.environment.name=="npm-publish") | .environment.id')"
if [ -z "$env_id" ]; then
  echo "run $run has no pending npm-publish deployment." >&2
  exit 1
fi

gh api -X POST "repos/$repo/actions/runs/$run/pending_deployments" \
  -F "environment_ids[]=$env_id" -f state=approved -f comment="approved from the terminal" >/dev/null
echo "approved npm-publish for $tag (run $run): https://github.com/$repo/actions/runs/$run"
