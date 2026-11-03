#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, unlinkSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

function qgBinaryName(platform = process.platform) {
  return platform === 'win32' ? 'qg.exe' : 'qg';
}

function helperShimNames(name, platform = process.platform) {
  if (platform === 'win32') {
    return [`${name}.cmd`, `${name}.exe`, name];
  }
  return [name];
}

function commandForShim(path, platform = process.platform) {
  if (platform === 'win32' && path.toLowerCase().endsWith('.cmd')) {
    return {
      command: process.env.ComSpec || 'cmd.exe',
      prefixArgs: ['/d', '/s', '/c', `"${path.replace(/"/g, '""')}"`],
    };
  }
  return { command: path, prefixArgs: [] };
}

function resolveHelper(packageRoot, name) {
  const binDir = join(packageRoot, 'node_modules', '.bin');
  for (const shimName of helperShimNames(name)) {
    const candidate = join(binDir, shimName);
    if (!existsSync(candidate)) continue;
    return {
      ...commandForShim(candidate),
      pathPrepend: [binDir],
    };
  }
  return null;
}

// Legacy path: helpers installed as npm dependencies of the wrapper. Current
// installs vendor the helpers inside the platform package instead (a helpers/
// directory next to the binary, found by the Go side on its own), so this
// manifest is usually empty — it only matters for a downloaded-binary install
// whose wrapper still carries helper dependencies.
function localControlHelperManifest(packageRoot) {
  const helpers = {};
  for (const name of ['agent-browser', 'tuistory']) {
    const helper = resolveHelper(packageRoot, name);
    if (helper) helpers[name] = helper;
  }
  if (Object.keys(helpers).length === 0) return '';
  return JSON.stringify({ version: 1, helpers });
}

// Mirrors internal/cli/observability.go parseDoctorArgs + writeDoctorHelp so the
// wrapper's missing-binary doctor fallback matches the Go doctor CLI surface.
const DOCTOR_HELP = `Uso:
  qg doctor [opções]

Roda as verificações de saúde do backend Go para configuração e provedor.

Opções:
      --json            Mostra o relatório em JSON
      --connectivity    Inclui a sondagem de conectividade do provedor quando disponível
  -h, --help            Mostra esta ajuda
`;

const EXIT_USAGE = 2;

function installedByBun() {
  if (process.env.QG_WRAPPER_SIMULATE_BUN === '1') {
    return true;
  }
  return process.execPath.includes('bun') || !!process.versions?.bun;
}

function bunRecoveryParagraph() {
  return (
    'Você instalou com Bun, que não roda scripts de ciclo de vida das dependências\n' +
    'por padrão. Autorize o pacote para rodar o postinstall bloqueado:\n' +
    '  bun pm trust @quebragalho-dev/cli       (instalação no projeto)\n' +
    '  bun pm -g trust @quebragalho-dev/cli    (instalação global)\n' +
    'Em versões do Bun sem `bun pm trust`, adicione\n' +
    '  "trustedDependencies": ["@quebragalho-dev/cli"]\n' +
    'ao package.json do projeto e reinstale.\n' +
    '\n'
  );
}

function buildFromSourceParagraph() {
  return (
    'Se não há binário pré-compilado para esta plataforma, compile do código-fonte:\n' +
    'https://github.com/quebragalho-dev/quebragalho-cli (go run ./cmd/qg, precisa de Go 1.26+).'
  );
}

function missingBinaryContextParagraph() {
  return (
    'Normalmente o npm o instala como dependência opcional de @quebragalho-dev/cli\n' +
    `(@quebragalho-dev/cli-${process.platform}-${process.arch}), e o wrapper também\n` +
    'consegue baixá-lo do GitHub Release quando está faltando.'
  );
}

function missingNativeRecoveryParagraphs(postinstallScript) {
  const ranByBun = installedByBun();
  return (
    missingBinaryContextParagraph() +
    '\n' +
    '\n' +
    'O que tentar:\n' +
    '  - reinstale sem omitir as dependências opcionais:\n' +
    '      npm install -g @quebragalho-dev/cli\n' +
    '  - rode o downloader manualmente (precisa de escrita na pasta do pacote):\n' +
    `      node "${postinstallScript}"\n` +
    '\n' +
    (ranByBun ? bunRecoveryParagraph() : '') +
    buildFromSourceParagraph()
  );
}

function formatGenericMissingNativeBinaryMessage(postinstallScript) {
  return (
    '[qg] Nenhum binário nativo disponível para esta instalação.\n' +
    missingNativeRecoveryParagraphs(postinstallScript)
  );
}

function parseDoctorArgs(args) {
  let json = false;
  for (const arg of args) {
    switch (arg) {
      case '-h':
      case '--help':
      case 'help':
        return { kind: 'help' };
      case '--json':
        json = true;
        break;
      case '--connectivity':
        break;
      default:
        return { kind: 'error', message: `flag do doctor desconhecida ${JSON.stringify(arg)}` };
    }
  }
  return { kind: 'run', json };
}

function missingNativeDoctorJSONReport(postinstallScript) {
  return {
    generatedAt: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z'),
    ok: false,
    checks: [
      {
        id: 'runtime.go',
        label: 'Go runtime',
        status: 'fail',
        message: 'Native qg binary is missing next to the npm wrapper.',
        details: {
          remedy: `node "${postinstallScript}"`,
        },
      },
    ],
  };
}

function missingNativeDoctorTextReport(postinstallScript) {
  return (
    'Relatório QG doctor (' +
    new Date().toISOString() +
    ')\n' +
    'Geral: reprovado\n' +
    '[reprovado] runtime.go - Binário nativo do qg não encontrado ao lado do wrapper npm.\n' +
    '  remedy: node "' +
    postinstallScript +
    '"\n' +
    '\n' +
    missingNativeRecoveryParagraphs(postinstallScript)
  );
}

// The platform payload is a version of @quebragalho-dev/cli itself, installed under
// an npm: alias (see docs/NPM_PACKAGING.md). The alias name is derived from
// process.platform/process.arch, so an unsupported platform simply fails to
// resolve and we fall through to the downloader.
function platformPackageBinary() {
  const aliasedName = `@quebragalho-dev/cli-${process.platform}-${process.arch}`;
  let manifestPath;
  try {
    manifestPath = createRequire(import.meta.url).resolve(`${aliasedName}/package.json`);
  } catch {
    return null;
  }
  const candidate = join(dirname(manifestPath), qgBinaryName());
  return existsSync(candidate) ? candidate : null;
}

// Mirrors the release matrix (internal/release/release.go): the platforms a
// GitHub Release asset exists for. android maps to the linux asset.
function releaseAssetAvailable() {
  const platform = { linux: 'linux', android: 'linux', darwin: 'macos', win32: 'windows' }[
    process.platform
  ];
  const arch = process.arch === 'x64' || process.arch === 'arm64' ? process.arch : null;
  if (!platform || !arch) return false;
  return true;
}

// Fallback for installs without a platform package (--omit=optional, package
// managers that skip optionalDependencies): fetch the binary next to the
// wrapper with the same checksum-verified downloader that used to run as
// postinstall. Deliberately NOT cached on failure — a transient network error
// self-heals on the next run — so this retries on every invocation until a
// binary is in place. Platforms with no release asset skip the attempt.
function downloadMissingBinary(packageRoot) {
  const downloadScript = join(packageRoot, 'scripts', 'postinstall.mjs');
  if (!existsSync(downloadScript) || !releaseAssetAvailable()) return;
  console.error(
    '[qg] pacote de plataforma ausente — baixando o binário nativo do GitHub Release (tenta de novo a cada execução até conseguir).',
  );
  spawnSync(process.execPath, [downloadScript], {
    stdio: ['ignore', 'inherit', 'inherit'],
  });
}

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

function resolveNativeBinary() {
  const fromPlatformPackage = platformPackageBinary();
  if (fromPlatformPackage) return fromPlatformPackage;
  const downloaded = join(packageRoot, qgBinaryName());
  if (existsSync(downloaded)) return downloaded;
  downloadMissingBinary(packageRoot);
  return existsSync(downloaded) ? downloaded : null;
}

// A npm install hands the actual upgrade to this wrapper: the native binary
// cannot replace its own .exe while it runs (Windows locks it), and a single
// flow covers every OS. The Go side writes {"version":"X"} to the handoff file
// below and exits 0; the wrapper installs globally, then reopens the same
// command line. The reopened qg verifies itself through
// QG_UPDATE_JUST_INSTALLED and reports a mismatch instead of looping.
function updateHandoffPath() {
  return join(tmpdir(), `qg-npm-update-${process.pid}-${Date.now()}.json`);
}

// Reads and removes a written handoff file, returning the requested version,
// or null when there is no install to perform (normal runs, failed installs,
// unreadable or malformed files).
function takeUpdateHandoff(path) {
  if (!path) return null;
  let raw;
  try {
    raw = readFileSync(path, 'utf8');
  } catch {
    return null;
  } finally {
    try {
      unlinkSync(path);
    } catch {
      /* best effort: a stale handoff file is harmless, the next run overwrites it */
    }
  }
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed.version === 'string' && parsed.version.length > 0) {
      return parsed.version;
    }
  } catch {
    /* fall through: malformed handoff means no install */
  }
  return null;
}

// npmPrefixFromPackageRoot infers the global npm prefix owning this wrapper:
// Unix keeps the package at <prefix>/lib/node_modules/@quebragalho-dev/cli,
// Windows at <prefix>\node_modules\@quebragalho-dev\cli. A Unix path without
// the lib segment is a local install, not a global prefix, so the bare
// node_modules suffix only counts for Windows-looking paths. Anything else
// returns null and the install below stays a bare `npm install -g`, exactly
// as before, instead of guessing.
function npmPrefixFromPackageRoot(root) {
  const normalized = root.replace(/\\/g, '/').replace(/\/+$/, '');
  const scopeSuffix = '/node_modules/@quebragalho-dev/cli';
  const libSuffix = '/lib' + scopeSuffix;
  if (normalized.endsWith(libSuffix)) {
    const prefix = normalized.slice(0, -libSuffix.length);
    return prefix === '' ? null : prefix;
  }
  const windowsLooking = root.includes('\\') || /^[A-Za-z]:/.test(normalized);
  if (normalized.endsWith(scopeSuffix) && windowsLooking) {
    const prefix = normalized.slice(0, -scopeSuffix.length);
    return prefix === '' ? null : prefix;
  }
  return null;
}

// Installs the new version in the given global prefix with npm's own output
// sent to stderr, so a --json handoff leaves stdout clean for the reopened
// process's single final JSON. Resolves npm the same way npm's own shims do:
// directly off PATH on POSIX, through ComSpec on Windows.
function installNpmUpdate(version, prefix) {
  const spec = `@quebragalho-dev/cli@${version}`;
  const args = ['install', '-g', '--prefix', prefix, spec];
  const stdio = ['inherit', 2, 2];
  if (process.platform === 'win32') {
    return spawnSync(process.env.ComSpec || 'cmd.exe', ['/d', '/s', '/c', 'npm', ...args], { stdio });
  }
  return spawnSync('npm', args, { stdio });
}

function reopenAfterNpmInstall(version) {
  const env = { ...process.env };
  delete env.QG_NPM_UPDATE_HANDOFF;
  env.QG_UPDATE_JUST_INSTALLED = version;
  const child = spawnSync(process.execPath, [process.argv[1], ...process.argv.slice(2)], { stdio: 'inherit', env });
  if (child.error) {
    console.error(`[qg] Não foi possível reabrir após a atualização: ${child.error.message}`);
    return 1;
  }
  if (child.signal) {
    process.kill(process.pid, child.signal);
  }
  return child.status ?? 1;
}

const nativePath = resolveNativeBinary();

if (!nativePath) {
  const postinstallScript = join(packageRoot, 'scripts', 'postinstall.mjs');

  // `qg doctor` is the diagnostic command: when the native binary is missing
  // it's the one invocation that must NOT bail with the generic wrapper error,
  // because that's exactly the blind alley issue #405 reports. Instead, surface
  // a doctor-shaped FAIL line for the runtime so the user's diagnostic finds the
  // real cause. We branch on a literal 'doctor' subcommand only (matching `qg
  // doctor` and `qg doctor --connectivity`), preserving the existing bail for
  // every other invocation (exec, providers list, TUI, --version, etc.).
  const argv = process.argv.slice(2);
  const isDoctor = argv.length > 0 && argv[0] === 'doctor';
  if (isDoctor) {
    const parsed = parseDoctorArgs(argv.slice(1));
    if (parsed.kind === 'help') {
      process.stdout.write(DOCTOR_HELP);
      process.exit(0);
    }
    if (parsed.kind === 'error') {
      process.stderr.write(`[qg] ${parsed.message}\n`);
      process.exit(EXIT_USAGE);
    }

    if (parsed.json) {
      process.stdout.write(JSON.stringify(missingNativeDoctorJSONReport(postinstallScript), null, 2) + '\n');
      process.exit(1);
    }

    process.stdout.write(missingNativeDoctorTextReport(postinstallScript) + '\n');
    process.exit(1);
  }

  console.error(formatGenericMissingNativeBinaryMessage(postinstallScript));
  process.exit(1);
}

const env = { ...process.env };
const localControlHelpers = localControlHelperManifest(packageRoot);
if (localControlHelpers) {
  env.QG_LOCAL_CONTROL_HELPERS = localControlHelpers;
} else {
  delete env.QG_LOCAL_CONTROL_HELPERS;
}
// The wrapper always offers a handoff path: if this run ends with an update
// choice, the Go side records the version here and exits, and the wrapper
// performs the global npm install that the running binary cannot do itself.
env.QG_NPM_UPDATE_HANDOFF = updateHandoffPath();

const child = spawnSync(nativePath, process.argv.slice(2), {
  stdio: 'inherit',
  env,
});

const handoffVersion = takeUpdateHandoff(env.QG_NPM_UPDATE_HANDOFF);

if (child.error) {
  console.error(`[qg] Não foi possível iniciar o binário: ${child.error.message}`);
  process.exit(1);
}

if (child.signal) {
  process.kill(process.pid, child.signal);
}

// An installed update takes effect here: install into the prefix owning this
// wrapper, then reopen the same command line. Without an inferable prefix a
// bare `npm install -g` would land in the default global prefix instead of
// this install, so print the manual command and reopen the current version
// instead of installing elsewhere. On npm failure print one line and still
// reopen — the current version opens instead of leaving the user without a
// working qg.
if (handoffVersion !== null && child.status === 0) {
  const prefix = npmPrefixFromPackageRoot(packageRoot);
  if (!prefix) {
    console.error(`[qg] Não foi possível determinar o prefixo npm desta instalação (${packageRoot}); para atualizar à mão, rode: npm install -g @quebragalho-dev/cli@${handoffVersion}. Abrindo a versão atual.`);
    process.exit(reopenAfterNpmInstall(handoffVersion));
  }
  const installed = installNpmUpdate(handoffVersion, prefix);
  if (installed.error || installed.status !== 0) {
    console.error(`[qg] Não foi possível instalar a versão ${handoffVersion} via npm; abrindo a versão atual. Para atualizar à mão, rode: npm install -g --prefix "${prefix}" @quebragalho-dev/cli@${handoffVersion}.`);
  }
  process.exit(reopenAfterNpmInstall(handoffVersion));
}

process.exit(child.status ?? 1);
