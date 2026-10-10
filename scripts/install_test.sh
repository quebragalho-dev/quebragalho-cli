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
cat "$ROOT/scripts/install.sh" | QG_VERSION="v$VERSION" \
QG_REPO="$REPO" \
QG_GITHUB_BASE_URL="file://$fake_base" \
QG_INSTALL_DIR="$install_dir" \
  bash -s >"$tmp/install.out" 2>"$tmp/install.err" \
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

ux_failures=0
check_ux() {
  local name="$1"
  shift
  if "$@"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name" >&2
    ux_failures=$((ux_failures + 1))
  fi
}

check_ux "progress visible in pipe" grep -q '100' "$tmp/install.err"
for stage in 'Resolvendo versão' 'Baixando' 'Verificando SHA-256' 'Instalando'; do
  check_ux "stage: $stage" grep -q "$stage" "$tmp/install.err"
done
check_ux "PATH instruction on its own line" grep -q '^  export PATH=' "$tmp/install.out"

shim="$tmp/shim"
mkdir -p "$shim" "$tmp/home"
cat >"$shim/curl" <<'EOF'
#!/usr/bin/env bash
printf 'download attempted\n' >"$DOWNLOAD_MARKER"
exit 98
EOF
chmod +x "$shim/curl"
printf 'blocked\n' >"$tmp/not-a-directory"
blocked="$tmp/not-a-directory/bin with space"
if cat "$ROOT/scripts/install.sh" | HOME="$tmp/home" PATH="$shim:$PATH" \
  DOWNLOAD_MARKER="$tmp/download-attempted" QG_INSTALL_DIR="$blocked" \
  QG_REPO='example/custom-repo' QG_VERSION='v9.8.7' \
  bash -s >"$tmp/blocked.out" 2>"$tmp/blocked.err"; then
  fail "unwritable destination succeeded"
fi
check_ux "preflight before download" test ! -e "$tmp/download-attempted"
check_ux "sudo command supplied" grep -q '| sudo bash -s --' "$tmp/blocked.err"
check_ux "local command assigns env to bash" grep -Fq "| QG_INSTALL_DIR=\"\$HOME/.local/bin\" bash -s --" "$tmp/blocked.err"
check_ux "custom repo retained" grep -q -- '--repo example/custom-repo' "$tmp/blocked.err"
check_ux "pinned version retained" grep -q -- '--version v9.8.7' "$tmp/blocked.err"
check_ux "local PATH hint supplied" grep -q '^  export PATH=' "$tmp/blocked.err"


if cat "$ROOT/scripts/install.sh" | HOME="$tmp/home" PATH="$tmp/home/.local/bin:$shim:$PATH" \
  DOWNLOAD_MARKER="$tmp/download-attempted" QG_INSTALL_DIR="$blocked" \
  bash -s >"$tmp/path-present.out" 2>"$tmp/path-present.err"; then
  fail "unwritable destination succeeded with local PATH"
fi
check_ux "no PATH hint when already present" test "$(grep -c '^  export PATH=' "$tmp/path-present.err" || true)" -eq 0

mkdir -p "$tmp/read-only"
chmod 500 "$tmp/read-only"
if [ ! -w "$tmp/read-only" ]; then
  rm -f "$tmp/download-attempted"
  if cat "$ROOT/scripts/install.sh" | HOME="$tmp/home" PATH="$shim:$PATH" \
    DOWNLOAD_MARKER="$tmp/download-attempted" QG_INSTALL_DIR="$tmp/read-only" \
    bash -s >"$tmp/read-only.out" 2>"$tmp/read-only.err"; then
    fail "read-only directory succeeded"
  fi
  check_ux "read-only directory rejected before download" test ! -e "$tmp/download-attempted"
  check_ux "read-only directory explains recovery" grep -q '| sudo bash -s --' "$tmp/read-only.err"
else
  echo "SKIP: read-only mode bits do not restrict this user"
fi
chmod 700 "$tmp/read-only"

mkdir -p "$tmp/scratch"
if cat "$ROOT/scripts/install.sh" | TMPDIR="$tmp/scratch" PATH="$shim:$PATH" \
  DOWNLOAD_MARKER="$tmp/download-attempted" QG_INSTALL_DIR="$tmp/failed-download" \
  QG_VERSION='v9.8.7' bash -s >"$tmp/download-fail.out" 2>"$tmp/download-fail.err"; then
  fail "download error was swallowed"
fi
check_ux "failed download does not install" test ! -e "$tmp/failed-download/qg"
check_ux "failed download cleans temporary files" test -z "$(ls -A "$tmp/scratch")"

recovery="$tmp/recovery"
mkdir -p "$recovery"
cat >"$recovery/curl" <<'EOF'
#!/usr/bin/env bash
cat <<'SCRIPT'
printf '%s\n' "${QG_INSTALL_DIR:-}" "$@" >"$RECOVERY_ARGS"
SCRIPT
EOF
cat >"$recovery/sudo" <<'EOF'
#!/usr/bin/env bash
exec "$@"
EOF
chmod +x "$recovery/curl" "$recovery/sudo"
custom_repo="example/repo with space;\$(touch $tmp/injected)"
custom_version="v1.2.3;\$(touch $tmp/injected)"
if cat "$ROOT/scripts/install.sh" | HOME="$tmp/home" QG_INSTALL_DIR="$blocked" \
  QG_REPO="$custom_repo" QG_VERSION="$custom_version" \
  bash -s >"$tmp/quoted.out" 2>"$tmp/quoted.err"; then
  fail "unwritable quoted destination succeeded"
fi
while IFS= read -r command_line; do
  HOME="$tmp/home" PATH="$recovery:$PATH" RECOVERY_ARGS="$tmp/recovery-args" \
    bash -c "$command_line"
  check_ux "recovery preserves quoted repo" grep -Fxq "$custom_repo" "$tmp/recovery-args"
  check_ux "recovery preserves quoted version" grep -Fxq "$custom_version" "$tmp/recovery-args"
  case "$command_line" in
    *'| sudo '*) check_ux "sudo preserves selected directory" grep -Fxq "$blocked" "$tmp/recovery-args" ;;
    *) check_ux "local recovery sets user directory" grep -Fxq "$tmp/home/.local/bin" "$tmp/recovery-args" ;;
  esac
done < <(sed -n 's/^  \(curl .*\)/\1/p' "$tmp/quoted.err")
check_ux "recovery metacharacters never execute" test ! -e "$tmp/injected"

[ "$ux_failures" -eq 0 ] || fail "$ux_failures UX checks failed"
