#!/usr/bin/env bash
set -euo pipefail

QG_REPO="${QG_REPO:-quebragalho-dev/quebragalho-cli}"
QG_VERSION="${QG_VERSION:-latest}"
install_dir_source=default
if [ -n "${QG_INSTALL_DIR:-}" ]; then
  install_dir_source=environment
fi
QG_INSTALL_DIR="${QG_INSTALL_DIR:-/usr/local/bin}"
QG_GITHUB_API="${QG_GITHUB_API:-https://api.github.com}"
QG_GITHUB_BASE_URL="${QG_GITHUB_BASE_URL:-https://github.com}"

usage() {
  cat <<'EOF'
Instala o QG a partir dos GitHub Releases.

Uso:
  scripts/install.sh [--version <versão>] [--repo <dono/repo>] [--install-dir <caminho>]

Sem pasta configurada, um padrão sem escrita cai para uma pasta do usuário com
escrita, preferindo candidatas que já estão no PATH.

Ambiente:
  QG_VERSION          Versão ou tag da release. Padrão: latest.
  QG_REPO             Repositório no GitHub. Padrão: quebragalho-dev/quebragalho-cli.
  QG_INSTALL_DIR      Pasta do binário qg. Padrão: /usr/local/bin.
  QG_GITHUB_API       URL base da API do GitHub. Padrão: https://api.github.com.
  QG_GITHUB_BASE_URL  URL base web do GitHub. Padrão: https://github.com.
  QG_ALLOW_INSECURE_DOWNLOAD
                      Com 1, aceita URLs base que não são https (só para testes locais).
EOF
}

fail() {
  echo "qg install: $*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version precisa de um valor"
      QG_VERSION="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || fail "--repo precisa de um valor"
      QG_REPO="$2"
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || fail "--install-dir precisa de um valor"
      QG_INSTALL_DIR="$2"
      install_dir_source=argument
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "argumento desconhecido: $1"
      ;;
  esac
done

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 é obrigatório"
}

# O .sha256 vem da mesma origem do pacote, então o TLS é o único controle real
# de integridade: só https, a menos que QG_ALLOW_INSECURE_DOWNLOAD=1 libere um
# espelho local de teste.
insecure_download_allowed() {
  [ "${QG_ALLOW_INSECURE_DOWNLOAD:-}" = "1" ]
}

require_https() {
  local url="$1"

  case "$url" in
    https://*) return 0 ;;
  esac
  insecure_download_allowed || fail "origem de download insegura recusada para $url: só https:// é permitido (defina QG_ALLOW_INSECURE_DOWNLOAD=1 para liberar em testes locais)"
}

# Mantém o curl em https também nos redirecionamentos.
secure_curl() {
  if insecure_download_allowed; then
    curl "$@"
  else
    curl --proto '=https' --proto-redir '=https' "$@"
  fi
}

parse_wget_redirect() {
  local hdr_file="$1"
  local line rest val status="" location=""

  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      "  HTTP/"*)
        rest="${line#  HTTP/}"
        rest="${rest#* }"
        status="${rest%% *}"
        location=""
        ;;
      "  "[Ll][Oo][Cc][Aa][Tt][Ii][Oo][Nn]:*)
        val="${line#*:}"
        val="${val#"${val%%[![:space:]]*}"}"
        val="${val%"${val##*[![:space:]]}"}"
        location="$val"
        ;;
    esac
  done < "$hdr_file"

  case "$status" in
    301|302|303|307|308)
      if [ -n "$location" ]; then
        printf '%s\n' "$location"
        return 0
      fi
      ;;
  esac
  return 1
}

resolve_redirect_url() {
  local base_url="$1"
  local location="$2"
  local scheme after_scheme authority path_part path_only base_dir

  case "$location" in
    [A-Za-z][A-Za-z0-9+.-]*:*)
      printf '%s\n' "$location"
      return 0
      ;;
  esac

  scheme="${base_url%%://*}"
  after_scheme="${base_url#*://}"
  authority="${after_scheme%%[/?#]*}"

  case "$location" in
    //*)
      printf '%s:%s\n' "$scheme" "$location"
      ;;
    /*)
      printf '%s://%s%s\n' "$scheme" "$authority" "$location"
      ;;
    *)
      path_part="${after_scheme#"$authority"}"
      path_only="${path_part%%[\?#]*}"
      case "$path_only" in
        */*) base_dir="${path_only%/*}/" ;;
        *) base_dir="/" ;;
      esac
      printf '%s://%s%s%s\n' "$scheme" "$authority" "$base_dir" "$location"
      ;;
  esac
}

# O --https-only do GNU wget vale só para modo recursivo (-r), não para 30x:
# quando há --max-redirect (GNU wget), inspeciona cada salto com --max-redirect=0;
# quando não há (busybox wget), segue com -S e confere todos os Location antes
# de aceitar o arquivo baixado.
wget_max_redirect_mode=""
wget_supports_max_redirect() {
  if [ -z "$wget_max_redirect_mode" ]; then
    case "$(wget --help 2>&1 || true)" in
      *--max-redirect*) wget_max_redirect_mode="yes" ;;
      *) wget_max_redirect_mode="no" ;;
    esac
  fi
  [ "$wget_max_redirect_mode" = "yes" ]
}

advance_wget_hop() {
  local base_url="$1"
  local status="$2"
  local location="$3"
  local location_count="$4"
  local hdr_file="$5"
  local output="$6"
  local resolved

  next_hop_url="$base_url"
  case "$status" in
    301|302|303|307|308)
      if [ "$location_count" -ne 1 ] || [ -z "$location" ]; then
        rm -f "$hdr_file" "$output"
        fail "redirecionamento inválido recusado ao baixar $base_url"
      fi
      case "$location" in
        *[[:cntrl:]]*)
          rm -f "$hdr_file" "$output"
          fail "redirecionamento inválido recusado ao baixar $base_url"
          ;;
      esac
      resolved="$(resolve_redirect_url "$base_url" "$location")"
      case "$resolved" in
        [Hh][Tt][Tt][Pp][Ss]://*)
          next_hop_url="https://${resolved#*://}"
          ;;
        *)
          rm -f "$hdr_file" "$output"
          fail "redirecionamento inseguro recusado para $resolved: só https:// é permitido (defina QG_ALLOW_INSECURE_DOWNLOAD=1 para liberar em testes locais)"
          ;;
      esac
      ;;
  esac
}

verify_wget_redirect_chain() {
  local current_url="$1"
  local hdr_file="$2"
  local output="$3"
  local line rest val status="" location="" location_count=0

  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      "  HTTP/"*)
        advance_wget_hop "$current_url" "$status" "$location" "$location_count" "$hdr_file" "$output"
        current_url="$next_hop_url"
        rest="${line#  HTTP/}"
        rest="${rest#* }"
        status="${rest%% *}"
        location=""
        location_count=0
        ;;
      "  "[Ll][Oo][Cc][Aa][Tt][Ii][Oo][Nn]:*)
        val="${line#*:}"
        val="${val#"${val%%[![:space:]]*}"}"
        val="${val%"${val##*[![:space:]]}"}"
        location="$val"
        location_count=$((location_count + 1))
        ;;
    esac
  done < "$hdr_file"
  advance_wget_hop "$current_url" "$status" "$location" "$location_count" "$hdr_file" "$output"
}

secure_wget() {
  local url="$1"
  local output="$2"
  shift 2

  if insecure_download_allowed; then
    wget "$@" -O "$output" "$url"
    return 0
  fi

  local hdr_file rc=0
  if ! wget_supports_max_redirect; then
    require_https "$url"
    hdr_file="$(mktemp "${tmp_dir:-${TMPDIR:-/tmp}}/wget-headers.XXXXXX")"
    if wget -q -S "$@" -O "$output" "$url" 2>"$hdr_file"; then
      rc=0
    else
      rc=$?
    fi
    verify_wget_redirect_chain "$url" "$hdr_file" "$output"
    if [ "$rc" -ne 0 ]; then
      cat "$hdr_file" >&2
      rm -f "$hdr_file" "$output"
      return "$rc"
    fi
    rm -f "$hdr_file"
    return 0
  fi

  local current_url="$url"
  local hops=0
  local location next_url

  while :; do
    require_https "$current_url"
    hdr_file="$(mktemp "${tmp_dir:-${TMPDIR:-/tmp}}/wget-headers.XXXXXX")"
    if wget --no-verbose --server-response --max-redirect=0 "$@" "$current_url" --output-document "$output" 2>"$hdr_file"; then
      rm -f "$hdr_file"
      return 0
    else
      rc=$?
    fi

    if location="$(parse_wget_redirect "$hdr_file")"; then
      rm -f "$hdr_file" "$output"
      case "$location" in
        *[[:cntrl:]]*)
          fail "redirecionamento inválido recusado ao baixar $current_url"
          ;;
      esac
      next_url="$(resolve_redirect_url "$current_url" "$location")"
      case "$next_url" in
        [Hh][Tt][Tt][Pp][Ss]://*)
          next_url="https://${next_url#*://}"
          ;;
        *)
          fail "redirecionamento inseguro recusado para $next_url: só https:// é permitido (defina QG_ALLOW_INSECURE_DOWNLOAD=1 para liberar em testes locais)"
          ;;
      esac
      hops=$((hops + 1))
      [ "$hops" -le 10 ] || fail "redirecionamentos demais ao baixar $url"
      current_url="$next_url"
      continue
    fi

    cat "$hdr_file" >&2
    rm -f "$hdr_file" "$output"
    return "$rc"
  done
}

download() {
  local url="$1"
  local output="$2"

  require_https "$url"
  if command -v curl >/dev/null 2>&1; then
    secure_curl --fail --location --show-error --progress-bar "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    secure_wget "$url" "$output"
  else
    fail "curl ou wget é obrigatório"
  fi
}

download_json() {
  local url="$1"
  local output="$2"

  require_https "$url"
  if command -v curl >/dev/null 2>&1; then
    secure_curl --fail --location --show-error --silent --header 'Accept: application/vnd.github+json' "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    secure_wget "$url" "$output" --header='Accept: application/vnd.github+json'
  else
    fail "curl ou wget é obrigatório"
  fi
}

detect_platform() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "macos" ;;
    *) fail "plataforma sem suporte: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "x64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) fail "arquitetura sem suporte: $(uname -m)" ;;
  esac
}

latest_tag() {
  local metadata_file="$1"
  local api_url="${QG_GITHUB_API%/}/repos/${QG_REPO}/releases/latest"
  local tag

  download_json "$api_url" "$metadata_file"
  tag="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$metadata_file" | head -n 1)"
  [ -n "$tag" ] || fail "não foi possível ler tag_name de $api_url"
  echo "$tag"
}

verify_checksum() {
  local checksum_file="$1"

  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "$checksum_file"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$checksum_file"
  else
    fail "shasum ou sha256sum é obrigatório"
  fi
}

# Identidade esperada do certificado Sigstore, em forma de expressão regular
# para o cosign: o workflow release-artifacts deste repositório na tag exata.
# Pontos escapados para a expressão só casar o próprio endereço.
sigstore_identity_regexp() {
  local tag="$1"
  local repo_escaped
  repo_escaped="$(printf '%s' "$QG_REPO" | sed 's/\./\\./g')"
  printf '^https://github\\.com/%s/\\.github/workflows/release-artifacts\\.yml@refs/tags/%s$' "$repo_escaped" "$tag"
}

verify_sigstore() {
  local archive_path="$1" bundle_path="$2" tag="$3"
  cosign verify-blob --bundle "$bundle_path" \
    --certificate-identity-regexp "$(sigstore_identity_regexp "$tag")" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    "$archive_path"
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
    install_file "$source_path" "$QG_INSTALL_DIR/$name" || fail "não foi possível instalar $name em $QG_INSTALL_DIR"
  fi
}

install_file() {
  install_tmp="$(mktemp "$QG_INSTALL_DIR/.qg.XXXXXX")" &&
    cp "$1" "$install_tmp" &&
    chmod 755 "$install_tmp" &&
    mv -f "$install_tmp" "$2" &&
    install_tmp=""
}

copy_optional_dir() {
  local name="$1"
  local source_path

  if source_path="$(find_extracted_entry "$extract_dir" "$name" "dir")"; then
    rm -rf "${QG_INSTALL_DIR:?}/$name"
    cp -R "$source_path" "$QG_INSTALL_DIR/$name"
  fi
}

directory_in_path() {
  case ":$PATH:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

prepare_install_dir() {
  local probe
  mkdir -p "$1" 2>/dev/null || return 1
  probe="$(mktemp "$1/.qg-install-write.XXXXXX" 2>/dev/null)" || return 1
  rm -f "$probe"
}

select_fallback_dir() {
  local pass candidate
  for pass in path outside; do
    for candidate in "$HOME/.local/bin" /opt/homebrew/bin "$HOME/.bin"; do
      if directory_in_path "$candidate"; then
        [ "$pass" = path ] || continue
      else
        [ "$pass" = outside ] || continue
      fi
      if prepare_install_dir "$candidate"; then
        printf 'qg install: %s indisponível; usando %s.\n' "$QG_INSTALL_DIR" "$candidate" >&2
        QG_INSTALL_DIR="$candidate"
        return 0
      fi
    done
  done
  return 1
}

path_hint() {
  local directory="$1"
  if ! directory_in_path "$directory"; then
    printf 'Para usar qg neste terminal, adicione o diretório ao PATH:\n'
    printf "  export PATH=%q:\"\$PATH\"\n" "$directory"
  fi
}

install_dir_error() {
  local installer_url='https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh'
  {
    printf 'qg install: não é possível escrever em %s.\n' "$QG_INSTALL_DIR"
    if [ "$install_dir_source" = default ]; then
      printf 'Nenhum diretório alternativo está gravável. Escolha outro com --install-dir.\n'
      exit 1
    fi
    printf 'Instale sem sudo no seu usuário:\n'
    printf "  curl -fsSL %s | QG_INSTALL_DIR=\"\$HOME/.local/bin\" bash -s -- --repo %q --version %q\n" "$installer_url" "$QG_REPO" "$QG_VERSION"
    path_hint "$HOME/.local/bin"
    if [ "$install_dir_source" = argument ]; then
      printf 'Ou, para instalar no diretório escolhido com permissão de administrador:\n'
      printf '  curl -fsSL %s | sudo bash -s -- --repo %q --version %q --install-dir %q\n' "$installer_url" "$QG_REPO" "$QG_VERSION" "$QG_INSTALL_DIR"
    fi
  } >&2
  exit 1
}

need_command uname
need_command sed
need_command tar
need_command mktemp

if ! prepare_install_dir "$QG_INSTALL_DIR"; then
  if [ "$install_dir_source" != default ] || ! select_fallback_dir; then
    install_dir_error
  fi
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/qg-install.XXXXXX")"
install_tmp=""
cleanup() {
  rm -rf "$tmp_dir"
  if [ -n "${install_tmp:-}" ]; then
    rm -f "$install_tmp"
  fi
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
bundle_name="${archive_name}.sigstore.json"
release_url="${QG_GITHUB_BASE_URL%/}/${QG_REPO}/releases/download/${tag}"
archive_path="$tmp_dir/$archive_name"
checksum_path="$tmp_dir/$checksum_name"
bundle_path="$tmp_dir/$bundle_name"
extract_dir="$tmp_dir/extract"
identity_regexp="$(sigstore_identity_regexp "$tag")"

echo "Instalando QG ${tag} para ${platform}-${arch}"
printf 'qg install: Baixando %s...\n' "$archive_name" >&2
download "${release_url}/${archive_name}" "$archive_path"
download "${release_url}/${checksum_name}" "$checksum_path"

if command -v cosign >/dev/null 2>&1; then
  printf 'qg install: Baixando assinatura Sigstore...\n' >&2
  download "${release_url}/${bundle_name}" "$bundle_path" || fail "esta release não publica o bundle Sigstore (${bundle_name}); a assinatura existe a partir da 0.0.22"
  printf 'qg install: Verificando assinatura Sigstore...\n' >&2
  verify_sigstore "$archive_path" "$bundle_path" "$tag" || fail "assinatura Sigstore inválida para ${archive_name}; a instalação foi recusada"
else
  printf 'qg install: cosign ausente, assinatura Sigstore não verificada (só SHA-256); baixe %s e rode: cosign verify-blob --bundle %s --certificate-identity-regexp %s --certificate-oidc-issuer https://token.actions.githubusercontent.com %s\n' "${release_url}/${bundle_name}" "'${identity_regexp}'" "$bundle_name" "$archive_name" >&2
fi

printf 'qg install: Verificando SHA-256...\n' >&2
(
  cd "$tmp_dir"
  verify_checksum "$checksum_name"
)

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"

binary_path="$(find_extracted_binary "$extract_dir")" || fail "pacote do release não trouxe o qg"

printf 'qg install: Instalando em %s...\n' "$QG_INSTALL_DIR" >&2
install_file "$binary_path" "$QG_INSTALL_DIR/qg" || fail "não foi possível instalar o qg em $QG_INSTALL_DIR; a versão anterior foi mantida"
# Real on-disk alias: same binary reachable as `quebragalho` (relative link, so
# a moved install dir keeps working).
ln -sf qg "$QG_INSTALL_DIR/quebragalho"
copy_optional_file "qg-linux-sandbox"
copy_optional_file "qg-seccomp"
copy_optional_dir "helpers"

installed_version=""
if ! installed_version="$("$QG_INSTALL_DIR/qg" --version 2>&1 | head -n 1)" \
  || [ -z "$installed_version" ]; then
  fail "$QG_INSTALL_DIR/qg --version não rodou — o binário instalado não funciona nesta plataforma"
fi

echo "Instalado $QG_INSTALL_DIR/qg ($installed_version)"
echo "Alias: $QG_INSTALL_DIR/quebragalho -> qg"

path_hint "$QG_INSTALL_DIR"
