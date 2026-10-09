#!/usr/bin/env bash
# Asserts the installer's default download URL points at the public release mirror.
# A curl stub records the first URL requested and fails, so no network is used.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/qg-install-mirror.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/stubbin"
cat >"$tmp/stubbin/curl" <<'STUB'
#!/usr/bin/env bash
for arg in "$@"; do
  case "$arg" in
    http*) printf '%s\n' "$arg" >>"$QG_URL_LOG"; break ;;
  esac
done
exit 22
STUB
chmod +x "$tmp/stubbin/curl"

QG_URL_LOG="$tmp/urls.log" \
PATH="$tmp/stubbin:$PATH" \
QG_VERSION="v0.0.1" \
QG_INSTALL_DIR="$tmp/bin" \
  bash "$ROOT/scripts/install.sh" >"$tmp/out" 2>&1 || true

if [ ! -s "$tmp/urls.log" ]; then
  echo "install_mirror_test: installer requested no URL" >&2
  exit 1
fi
first="$(head -n 1 "$tmp/urls.log")"
case "$first" in
  https://github.com/quebragalho-dev/qg-releases/releases/download/v0.0.1/*) ;;
  *) echo "install_mirror_test: default URL = $first, want qg-releases mirror v0.0.1" >&2; exit 1 ;;
esac
echo "install_mirror_test: ok"
