#!/usr/bin/env node
// postinstall: fetch the prebuilt `qg` binary for this platform from the
// matching GitHub Release and place it next to bin/qg.js, which execs it.
//
// Mirrors scripts/install.sh and scripts/install.ps1. The asset-name scheme is
// the source of truth in internal/release/release.go:
//   qg-v{version}-{linux|macos|windows}-{x64|arm64}.{tar.gz|zip}  (+ .sha256)
//
// Safety: HTTPS-only download from the pinned repo, SHA-256 verification against
// the release's own .sha256, and extraction that never trusts archive paths —
// we extract to a temp dir and copy only known binary basenames into place, so a
// crafted archive cannot write outside the package (no zip-slip).
//
// Env overrides (testing / mirrors / locked-down installs):
//   QG_SKIP_DOWNLOAD=1      skip entirely, exit 0 (wrapper will guide if run)
//   QG_INSTALL_DRY_RUN=1    print the resolved plan as JSON, no network, exit 0
//   QG_INSTALL_PLATFORM=…   override process.platform (linux|darwin|win32|android)
//   QG_INSTALL_ARCH=…       override process.arch (x64|arm64)
//   QG_REPO=owner/name      override the GitHub repo (default quebragalho-dev/quebragalho-cli)
//   QG_GITHUB_BASE_URL=…    override the download host (default https://github.com)

import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const pkg = JSON.parse(readFileSync(join(packageRoot, 'package.json'), 'utf8'));
const VERSION = pkg.version;

const REPO = process.env.QG_REPO || 'quebragalho-dev/quebragalho-cli';
const BASE = (process.env.QG_GITHUB_BASE_URL || 'https://github.com').replace(/\/+$/, '');
// The .sha256 is fetched from the same origin as the archive, so TLS authenticity
// is the only real integrity control — require https unless explicitly overridden
// for local testing (e.g. a localhost mirror in tests).
const ALLOW_INSECURE = process.env.QG_ALLOW_INSECURE_DOWNLOAD === '1';
const MAX_DOWNLOAD_BYTES = 512 * 1024 * 1024;

function fail(message) {
  console.error(`[qg] ${message}`);
  process.exit(1);
}

function warnSkip(message) {
  // Exit 0 so an unsupported platform or an opt-out does not break `npm install`;
  // bin/qg.js reports a clear "no native binary" message if the user runs qg.
  console.error(`[qg] ${message}`);
  process.exit(0);
}

// Maps mirror internal/release/release.go (ReleasePlatform / ReleaseArch).
function resolvePlatform(p) {
  switch (p) {
    case 'linux':
    case 'android':
      return 'linux';
    case 'darwin':
      return 'macos';
    case 'win32':
      return 'windows';
    default:
      return null;
  }
}

function resolveArch(a) {
  switch (a) {
    case 'x64':
      return 'x64';
    case 'arm64':
      return 'arm64';
    default:
      return null;
  }
}

if (process.env.QG_SKIP_DOWNLOAD === '1') {
  warnSkip('QG_SKIP_DOWNLOAD=1 definido — pulando o download do binário nativo.');
}

const rawPlatform = process.env.QG_INSTALL_PLATFORM || process.platform;
const rawArch = process.env.QG_INSTALL_ARCH || process.arch;
const platform = resolvePlatform(rawPlatform);
const arch = resolveArch(rawArch);

if (!platform || !arch) {
  warnSkip(
    `sem binário pré-compilado para ${rawPlatform}/${rawArch}. Para compilar do código-fonte: ` +
      `https://github.com/${REPO} (go run ./cmd/qg).`,
  );
}


const ext = platform === 'windows' ? 'zip' : 'tar.gz';
const binaryName = platform === 'windows' ? 'qg.exe' : 'qg';
const tag = `v${VERSION}`;
const assetName = `qg-v${VERSION}-${platform}-${arch}.${ext}`;
const assetUrl = `${BASE}/${REPO}/releases/download/${tag}/${assetName}`;
const checksumUrl = `${assetUrl}.sha256`;

// Extra binaries the archive may carry; copied when present so the platform
// sandbox finds its adjacent helpers (tier-1 discovery), but never required.
const optionalBinaries =
  platform === 'windows'
    ? ['qg-windows-command-runner.exe', 'qg-windows-sandbox-setup.exe']
    : platform === 'linux'
      ? ['qg-linux-sandbox', 'qg-seccomp']
      : [];

if (process.env.QG_INSTALL_DRY_RUN === '1') {
  process.stdout.write(
    JSON.stringify({
      version: VERSION,
      platform,
      arch,
      tag,
      assetName,
      assetUrl,
      checksumUrl,
      binaryName,
      optionalBinaries,
    }) + '\n',
  );
  process.exit(0);
}

// Idempotent: skip if the binary for this exact version is already in place.
const markerPath = join(packageRoot, '.qg-binary-version');
const installedBinary = join(packageRoot, binaryName);
if (
  existsSync(installedBinary) &&
  existsSync(markerPath) &&
  readFileSync(markerPath, 'utf8').trim() === VERSION
) {
  process.exit(0);
}

async function download(url) {
  if (!ALLOW_INSECURE && !url.startsWith('https://')) {
    fail(
      `origem de download insegura recusada para ${url}: só https:// é permitido ` +
        `(defina QG_ALLOW_INSECURE_DOWNLOAD=1 para liberar em testes locais).`,
    );
  }
  let response;
  try {
    response = await fetch(url, { redirect: 'follow' });
  } catch (error) {
    fail(`não foi possível baixar ${url}: ${error.message}`);
  }
  // Catch an https->http downgrade across redirects: the verified bytes must
  // still have arrived over TLS for the same-origin checksum to mean anything.
  if (!ALLOW_INSECURE && response.url && !response.url.startsWith('https://')) {
    fail(`redirecionamento inseguro recusado para ${response.url}`);
  }
  if (!response.ok) {
    fail(
      `não foi possível baixar (HTTP ${response.status}) de ${url} (tag do release ${tag}). ` +
        `Se ainda não existe release, ou se a tag e a versão do package.json divergem, ` +
        `compile do código-fonte: https://github.com/${REPO} (go run ./cmd/qg).`,
    );
  }
  const declared = Number(response.headers.get('content-length') || 0);
  if (declared && declared > MAX_DOWNLOAD_BYTES) {
    fail(`download grande demais recusado (${declared} bytes) de ${url}`);
  }
  const buffer = Buffer.from(await response.arrayBuffer());
  if (buffer.length > MAX_DOWNLOAD_BYTES) {
    fail(`download de ${url} passou de ${MAX_DOWNLOAD_BYTES} bytes`);
  }
  return buffer;
}

// Parse a sha256sum-style file. Anchored per line, and when the line carries a
// filename field it must equal the requested asset — mirrors the Go
// ParseSHA256Checksum so a checksum file cannot misattribute a digest.
function parseSha256(text, wantName) {
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) continue;
    const match = line.match(/^([a-fA-F0-9]{64})(?:\s+\*?(.+))?$/);
    if (!match) continue;
    const [, hex, name] = match;
    if (!name || name.trim() === wantName) {
      return hex.toLowerCase();
    }
  }
  fail(`sem digest SHA-256 para ${wantName} em ${wantName}.sha256`);
}

function findByBasename(dir, name) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      const nested = findByBasename(full, name);
      if (nested) return nested;
    } else if (entry.name === name) {
      return full;
    }
  }
  return null;
}

// extract runs from `workDir` and passes RELATIVE names so no Windows drive
// letter (e.g. C:\) appears in tar's archive argument — GNU tar otherwise reads
// `C:foo` as a remote `host:path` and fails. archiveName and destName are both
// relative to workDir.
function extract(workDir, archiveName, destName) {
  const opts = { stdio: 'inherit', cwd: workDir };
  if (ext === 'zip') {
    // Windows 10+ ships bsdtar as tar.exe, which extracts zips; fall back to
    // PowerShell Expand-Archive if tar is unavailable or cannot read the zip.
    const viaTar = spawnSync('tar', ['-xf', archiveName, '-C', destName], opts);
    if (viaTar.status === 0) return;
    const absArchive = join(workDir, archiveName);
    const absDest = join(workDir, destName);
    const viaPwsh = spawnSync(
      'powershell',
      [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        `Expand-Archive -LiteralPath '${absArchive.replace(/'/g, "''")}' ` +
          `-DestinationPath '${absDest.replace(/'/g, "''")}' -Force`,
      ],
      { stdio: 'inherit' },
    );
    if (viaPwsh.status !== 0) fail(`não foi possível extrair ${assetName}`);
    return;
  }
  const viaTar = spawnSync('tar', ['-xzf', archiveName, '-C', destName], opts);
  if (viaTar.status !== 0) fail(`não foi possível extrair ${assetName} (tar saiu com ${viaTar.status})`);
}

async function main() {
  const archiveBuffer = await download(assetUrl);
  const checksumText = (await download(checksumUrl)).toString('utf8');
  const expected = parseSha256(checksumText, assetName);
  const actual = createHash('sha256').update(archiveBuffer).digest('hex');
  if (actual !== expected) {
    fail(`checksum não confere para ${assetName}: esperado ${expected}, recebido ${actual}`);
  }

  const tempDir = mkdtempSync(join(tmpdir(), 'qg-install-'));
  try {
    const archivePath = join(tempDir, assetName);
    writeFileSync(archivePath, archiveBuffer);
    const extractDir = join(tempDir, 'extracted');
    mkdirSync(extractDir);
    extract(tempDir, assetName, 'extracted');

    // Copy only known basenames into the package root. We never honor
    // archive-relative paths, so a crafted entry cannot escape the package.
    const primarySource = findByBasename(extractDir, binaryName);
    if (!primarySource) fail(`pacote ${assetName} não trouxe ${binaryName}`);
    copyFileSync(primarySource, installedBinary);
    if (platform !== 'windows') chmodSync(installedBinary, 0o755);

    for (const name of optionalBinaries) {
      const source = findByBasename(extractDir, name);
      if (!source) {
        console.error(
          `[qg] aviso: helper opcional ${name} não estava em ${assetName}; ` +
            `o ambiente isolado pode rodar com isolamento reduzido.`,
        );
        continue;
      }
      const dest = join(packageRoot, name);
      copyFileSync(source, dest);
      if (platform !== 'windows') chmodSync(dest, 0o755);
    }

    writeFileSync(markerPath, VERSION + '\n');
    const sizeKb = Math.round(statSync(installedBinary).size / 1024);
    console.error(`[qg] ${binaryName} ${VERSION} instalado (${platform}-${arch}, ${sizeKb} KB).`);
  } finally {
    rmSync(tempDir, { recursive: true, force: true });
  }
}

main().catch((error) => fail(error.message));
