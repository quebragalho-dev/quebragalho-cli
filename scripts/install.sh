#!/usr/bin/env bash
set -euo pipefail

QG_REPO="${QG_REPO:-quebragalho-dev/qg-releases}"
QG_VERSION="${QG_VERSION:-latest}"
QG_INSTALL_DIR="${QG_INSTALL_DIR:-/usr/local/bin}"
QG_GITHUB_API="${QG_GITHUB_API:-https://api.github.com}"
QG_GITHUB_BASE_URL="${QG_GITHUB_BASE_URL:-https://github.com}"

usage() {
  cat <<'EOF'
Install QG from GitHub Releases.

Usage:
  scripts/install.sh [--version <version>] [--repo <owner/repo>] [--install-dir <path>]

Environment:
  QG_VERSION          Release version or tag. Defaults to latest.
  QG_REPO             GitHub repository. Defaults to quebragalho-dev/qg-releases.
  QG_INSTALL_DIR      Directory for the qg binary. Defaults to /usr/local/bin.
  QG_GITHUB_API       GitHub API base URL. Defaults to https://api.github.com.
  QG_GITHUB_BASE_URL  GitHub web base URL. Defaults to https://github.com.
EOF
}

fail() {
  echo "qg install: $*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      QG_VERSION="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || fail "--repo requires a value"
      QG_REPO="$2"
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || fail "--install-dir requires a value"
      QG_INSTALL_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

download() {
  local url="$1"
  local output="$2"

  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --show-error --progress-bar "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget "$url" --output-document "$output"
  else
    fail "curl or wget is required"
  fi
}

download_json() {
  local url="$1"
  local output="$2"

  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --show-error --silent --header 'Accept: application/vnd.github+json' "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget --quiet --header='Accept: application/vnd.github+json' "$url" --output-document "$output"
  else
    fail "curl or wget is required"
  fi
}

detect_platform() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "macos" ;;
    *) fail "unsupported platform: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "x64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

latest_tag() {
  local metadata_file="$1"
  local api_url="${QG_GITHUB_API%/}/repos/${QG_REPO}/releases/latest"
  local tag

  download_json "$api_url" "$metadata_file"
  tag="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$metadata_file" | head -n 1)"
  [ -n "$tag" ] || fail "could not read tag_name from $api_url"
  echo "$tag"
}

verify_checksum() {
  local checksum_file="$1"

  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "$checksum_file"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$checksum_file"
  else
    fail "shasum or sha256sum is required"
  fi
}

find_extracted_entry() {
  local root="$1"
  local name="$2"
  local kind="$3"
  local candidate

  if [ "$kind" = "dir" ] && [ -d "$root/$name" ]; then
    echo "$root/$name"
    return 0
  fi
  if [ "$kind" = "file" ] && [ -f "$root/$name" ]; then
    echo "$root/$name"
    return 0
  fi

  for candidate in "$root"/*/"$name"; do
    if [ "$kind" = "dir" ] && [ -d "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
    if [ "$kind" = "file" ] && [ -f "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
  done

  return 1
}

find_extracted_binary() {
  find_extracted_entry "$1" "qg" "file"
}

copy_optional_file() {
  local name="$1"
  local source_path

  if source_path="$(find_extracted_entry "$extract_dir" "$name" "file")"; then
    cp "$source_path" "$QG_INSTALL_DIR/$name"
    chmod 755 "$QG_INSTALL_DIR/$name"
  fi
}

copy_optional_dir() {
  local name="$1"
  local source_path

  if source_path="$(find_extracted_entry "$extract_dir" "$name" "dir")"; then
    rm -rf "${QG_INSTALL_DIR:?}/$name"
    cp -R "$source_path" "$QG_INSTALL_DIR/$name"
  fi
}

path_hint() {
  local directory="$1"
  case ":$PATH:" in
    *":$directory:"*) ;;
    *)
      printf 'Para usar qg neste terminal, adicione o diretório ao PATH:\n'
      printf "  export PATH=%q:\"\$PATH\"\n" "$directory"
      ;;
  esac
}

install_dir_error() {
  local installer_url='https://raw.githubusercontent.com/quebragalho-dev/qg-releases/main/scripts/install.sh'
  {
    printf 'qg install: não é possível escrever em %s.\n' "$QG_INSTALL_DIR"
    printf 'Instale sem sudo no seu usuário:\n'
    printf "  curl -fsSL %s | QG_INSTALL_DIR=\"\$HOME/.local/bin\" bash -s -- --repo %q --version %q\n" "$installer_url" "$QG_REPO" "$QG_VERSION"
    path_hint "$HOME/.local/bin"
    printf 'Ou, para instalar no diretório escolhido com permissão de administrador:\n'
    printf '  curl -fsSL %s | sudo bash -s -- --repo %q --version %q --install-dir %q\n' "$installer_url" "$QG_REPO" "$QG_VERSION" "$QG_INSTALL_DIR"
  } >&2
  exit 1
}

need_command uname
need_command sed
need_command tar
need_command mktemp

if ! mkdir -p "$QG_INSTALL_DIR" 2>/dev/null || [ ! -w "$QG_INSTALL_DIR" ]; then
  install_dir_error
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/qg-install.XXXXXX")"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

printf 'qg install: Resolvendo versão...\n' >&2
if [ "$QG_VERSION" = "latest" ]; then
  tag="$(latest_tag "$tmp_dir/latest.json")"
else
  case "$QG_VERSION" in
    v*) tag="$QG_VERSION" ;;
    *) tag="v$QG_VERSION" ;;
  esac
fi

version="${tag#v}"
platform="$(detect_platform)"
arch="$(detect_arch)"
archive_name="qg-v${version}-${platform}-${arch}.tar.gz"
checksum_name="${archive_name}.sha256"
release_url="${QG_GITHUB_BASE_URL%/}/${QG_REPO}/releases/download/${tag}"
archive_path="$tmp_dir/$archive_name"
checksum_path="$tmp_dir/$checksum_name"
extract_dir="$tmp_dir/extract"

echo "Installing QG ${tag} for ${platform}-${arch}"
printf 'qg install: Baixando %s...\n' "$archive_name" >&2
download "${release_url}/${archive_name}" "$archive_path"
download "${release_url}/${checksum_name}" "$checksum_path"

printf 'qg install: Verificando SHA-256...\n' >&2
(
  cd "$tmp_dir"
  verify_checksum "$checksum_name"
)

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"

binary_path="$(find_extracted_binary "$extract_dir")" || fail "release archive did not contain qg"

printf 'qg install: Instalando em %s...\n' "$QG_INSTALL_DIR" >&2
cp "$binary_path" "$QG_INSTALL_DIR/qg"
chmod 755 "$QG_INSTALL_DIR/qg"
# Real on-disk alias: same binary reachable as `quebragalho` (relative link, so
# a moved install dir keeps working).
ln -sf qg "$QG_INSTALL_DIR/quebragalho"
copy_optional_file "qg-linux-sandbox"
copy_optional_file "qg-seccomp"
copy_optional_dir "helpers"

installed_version=""
if ! installed_version="$("$QG_INSTALL_DIR/qg" --version 2>&1 | head -n 1)" \
  || [ -z "$installed_version" ]; then
  fail "$QG_INSTALL_DIR/qg --version did not run — the installed binary does not work on this platform"
fi

echo "Installed $QG_INSTALL_DIR/qg ($installed_version)"
echo "Alias: $QG_INSTALL_DIR/quebragalho -> qg"

path_hint "$QG_INSTALL_DIR"
