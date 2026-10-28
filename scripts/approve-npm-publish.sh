#!/usr/bin/env bash
# Approve a publish deployment gate from the terminal.
#
#   scripts/approve-npm-publish.sh v0.0.10                                     # CLI release
#   scripts/approve-npm-publish.sh sdk-v0.1.0 sdk-publish.yml sdk-publish      # SDKs (npm + PyPI)
#
# The gate stays on: this is the same "Review deployments -> Approve and deploy"
# click, done through the API with the caller's own gh login, so only a required
# reviewer can run it. Set QG_REPO=owner/name to target a fork.
set -euo pipefail

repo="${QG_REPO:-quebragalho-dev/quebragalho-cli}"
tag="${1:?usage: $0 <tag> [workflow] [environment]}"
workflow="${2:-release-artifacts.yml}"
environment="${3:-npm-publish}"

run="$(gh run list --repo "$repo" --workflow "$workflow" --branch "$tag" \
  --json databaseId,status -q 'map(select(.status=="waiting"))[0].databaseId // empty')"
if [ -z "$run" ]; then
  echo "no $workflow run for $tag is waiting for approval (still packaging, or already decided)." >&2
  exit 1
fi

env_id="$(gh api "repos/$repo/actions/runs/$run/pending_deployments" \
  -q '.[] | select(.environment.name=="'"$environment"'") | .environment.id')"
if [ -z "$env_id" ]; then
  echo "run $run has no pending $environment deployment." >&2
  exit 1
fi

gh api -X POST "repos/$repo/actions/runs/$run/pending_deployments" \
  -F "environment_ids[]=$env_id" -f state=approved -f comment="approved from the terminal" >/dev/null
echo "approved $environment for $tag (run $run): https://github.com/$repo/actions/runs/$run"
