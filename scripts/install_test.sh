#!/usr/bin/env bash
# E2E for scripts/install.sh: serves the host archive from a local file://
# GitHub-style layout, installs into a temp dir, and asserts the binary, the
# real `quebragalho` alias, and `--version`. Never touches /usr/local/bin and
# never needs the network.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO="${QG_REPO:-quebragalho-dev/quebragalho-cli}"
VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT/package.json" | head -n 1)"
[ -n "$VERSION" ] || { echo "install_test: cannot read version from package.json" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) platform="macos" ;;
  Linux) platform="linux" ;;
  *) echo "install_test: unsupported platform $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="x64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "install_test: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

archive="qg-v${VERSION}-${platform}-${arch}.tar.gz"
release_dir="$ROOT/dist/release"
if [ ! -f "$release_dir/$archive" ] || [ ! -f "$release_dir/$archive.sha256" ]; then
  echo "install_test: $archive missing — running 'make release' first"
  make -C "$ROOT" release
fi
[ -f "$release_dir/$archive" ] || { echo "install_test: $archive still missing after make release" >&2; exit 1; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/qg-install-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

# GitHub-style layout for a pinned release: file://<base>/<repo>/releases/download/<tag>/...
fake_base="$tmp/fake-github"
download_dir="$fake_base/$REPO/releases/download/v$VERSION"
mkdir -p "$download_dir"
cp "$release_dir/$archive" "$release_dir/$archive.sha256" "$download_dir/"

install_dir="$tmp/bin"
QG_VERSION="v$VERSION" \
QG_REPO="$REPO" \
QG_GITHUB_BASE_URL="file://$fake_base" \
QG_INSTALL_DIR="$install_dir" \
  bash "$ROOT/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err" \
  || { echo "install_test: install.sh failed"; cat "$tmp/install.out" "$tmp/install.err" >&2; exit 1; }

fail() { echo "install_test: $1" >&2; exit 1; }

[ -f "$install_dir/qg" ] || fail "qg binary missing after install"
[ -x "$install_dir/qg" ] || fail "qg binary is not executable"
[ -L "$install_dir/quebragalho" ] || fail "quebragalho alias is not a symlink"
alias_target="$(readlink "$install_dir/quebragalho")"
[ "$alias_target" = "qg" ] || fail "alias target = $alias_target, want relative qg"

version_out="$("$install_dir/qg" --version | head -n 1)"
case "$version_out" in
  qg*) ;;
  *) fail "qg --version printed: $version_out" ;;
esac
alias_out="$("$install_dir/quebragalho" --version | head -n 1)"
case "$alias_out" in
  qg*) ;;
  *) fail "quebragalho --version printed: $alias_out" ;;
esac

grep -q "Installed $install_dir/qg" "$tmp/install.out" || fail "success line missing from output"
grep -q "qg" "$tmp/install.out" || fail "version not echoed"

echo "PASS: install.sh -> $install_dir/qg ($version_out) + alias quebragalho -> qg"
