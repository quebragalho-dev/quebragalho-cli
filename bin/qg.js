#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, unlinkSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The npm package moved from @quebragalho-dev/cli to @quebragalho/cli. One
// bridge release also went out under the old name (docs/NPM_PACKAGING.md);
// installed under it, this wrapper moves the install to the new name.
const PACKAGE_NAME = '@quebragalho/cli';
const LEGACY_PACKAGE_NAME = '@quebragalho-dev/cli';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

// The name and version this wrapper was installed under, from the
// package.json beside it: the platform alias, the npm prefix and every
// reinstall hint follow the name.
function readOwnManifest(root) {
  try {
    const parsed = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
    return {
      name: typeof parsed.name === 'string' && parsed.name.length > 0 ? parsed.name : PACKAGE_NAME,
      version: typeof parsed.version === 'string' ? parsed.version : '',
    };
  } catch {
    return { name: PACKAGE_NAME, version: '' };
  }
}

const ownManifest = readOwnManifest(packageRoot);
const ownPackageName = ownManifest.name;
const installedUnderLegacyName = ownPackageName === LEGACY_PACKAGE_NAME;

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
    `  bun pm trust ${ownPackageName}       (instalação no projeto)\n` +
    `  bun pm -g trust ${ownPackageName}    (instalação global)\n` +
    'Em versões do Bun sem `bun pm trust`, adicione\n' +
    `  "trustedDependencies": ["${ownPackageName}"]\n` +
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
    `Normalmente o npm o instala como dependência opcional de ${ownPackageName}\n` +
    `(${ownPackageName}-${process.platform}-${process.arch}), e o wrapper também\n` +
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
    `      npm install -g ${ownPackageName}\n` +
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

// The platform payload is a version of this package itself, installed under
// an npm: alias (see docs/NPM_PACKAGING.md). The alias name is derived from
// process.platform/process.arch, so an unsupported platform simply fails to
// resolve and we fall through to the downloader.
function platformPackageBinary() {
  const aliasedName = `${ownPackageName}-${process.platform}-${process.arch}`;
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
// Unix keeps the package at <prefix>/lib/node_modules/<name>, Windows at
// <prefix>\node_modules\<name>. A Unix path without the lib segment is a
// local install, not a global prefix, so the bare node_modules suffix only
// counts for Windows-looking paths. Anything else returns null and the
// install below stays a bare `npm install -g`, exactly as before, instead of
// guessing.
function npmPrefixFromPackageRoot(root, packageName) {
  const normalized = root.replace(/\\/g, '/').replace(/\/+$/, '');
  const scopeSuffix = `/node_modules/${packageName}`;
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

// Runs npm with its own output sent to stderr, so a --json handoff leaves
// stdout clean for the reopened process's single final JSON. Resolves npm the
// same way npm's own shims do: directly off PATH on POSIX, through ComSpec on
// Windows.
function runNpm(args) {
  const stdio = ['inherit', 2, 2];
  if (process.platform === 'win32') {
    return spawnSync(process.env.ComSpec || 'cmd.exe', ['/d', '/s', '/c', 'npm', ...args], { stdio });
  }
  return spawnSync('npm', args, { stdio });
}

function npmSucceeded(result) {
  return !result.error && result.status === 0;
}

// Installs the new version in the given global prefix.
function installNpmUpdate(version, prefix) {
  return runNpm(['install', '-g', '--prefix', prefix, `${ownPackageName}@${version}`]);
}

function quotedPrefixFlag(prefix) {
  return `--prefix "${prefix}"`;
}

// The two commands that move an install from the legacy name to the current
// one, one per line so each pastes into any shell (Windows PowerShell 5.1 has
// no &&).
function manualMoveCommands(prefix) {
  const flag = prefix ? ` ${quotedPrefixFlag(prefix)}` : '';
  return `  npm uninstall -g${flag} ${LEGACY_PACKAGE_NAME}\n  npm install -g${flag} ${PACKAGE_NAME}`;
}

// moveToCurrentName replaces this install of the legacy name with the current
// name at the given version, in the same prefix. The old package is removed
// first: both own the qg bin, npm refuses the second install with EEXIST, and
// a forced install would lose the bin when the old package is removed later.
// If the new install fails, the old one is put back at this version so qg
// keeps working.
//
// Returns { wrapper } with the new wrapper's path after a move,
// { wrapper: null, kept: true } when the install is still (or again) the
// legacy one, and { wrapper: null, kept: false } when qg is left uninstalled.
function moveToCurrentName(version, prefix) {
  console.error(`[qg] O qg mudou de nome no npm: trocando ${LEGACY_PACKAGE_NAME} por ${PACKAGE_NAME}.`);
  const removed = runNpm(['uninstall', '-g', '--prefix', prefix, LEGACY_PACKAGE_NAME]);
  if (!npmSucceeded(removed)) {
    console.error(`[qg] Não foi possível remover ${LEGACY_PACKAGE_NAME}; o qg continua nele. Para trocar à mão, rode:\n${manualMoveCommands(prefix)}`);
    return { wrapper: null, kept: true };
  }
  const installed = runNpm(['install', '-g', '--prefix', prefix, `${PACKAGE_NAME}@${version}`]);
  if (npmSucceeded(installed)) {
    // Same layout as this install, one package over: <...>/node_modules/<name>.
    const currentRoot = join(dirname(dirname(packageRoot)), ...PACKAGE_NAME.split('/'));
    return { wrapper: join(currentRoot, 'bin', 'qg.js') };
  }
  const restoreSpec = ownManifest.version ? `${LEGACY_PACKAGE_NAME}@${ownManifest.version}` : LEGACY_PACKAGE_NAME;
  const restored = runNpm(['install', '-g', '--prefix', prefix, restoreSpec]);
  if (npmSucceeded(restored)) {
    console.error(`[qg] Não foi possível instalar ${PACKAGE_NAME}; o qg continua em ${LEGACY_PACKAGE_NAME}. Para trocar à mão, rode:\n${manualMoveCommands(prefix)}`);
    return { wrapper: null, kept: true };
  }
  console.error(`[qg] Não foi possível instalar ${PACKAGE_NAME} nem reinstalar ${LEGACY_PACKAGE_NAME}. Para voltar a ter o qg, rode:\n  npm install -g ${quotedPrefixFlag(prefix)} ${PACKAGE_NAME}`);
  return { wrapper: null, kept: false };
}

// Reopens the same command line after an npm install. moveTried marks a
// reopen after a move to the current name that did not happen, so the
// reopened legacy wrapper does not try it again: the Go side then reports the
// update that did not take, instead of the two retrying in a loop.
function reopenAfterNpmInstall(version, wrapperPath = process.argv[1], moveTried = false) {
  const env = { ...process.env };
  delete env.QG_NPM_UPDATE_HANDOFF;
  env.QG_UPDATE_JUST_INSTALLED = version;
  if (moveTried) {
    env.QG_NPM_MOVE_TRIED = '1';
  } else {
    delete env.QG_NPM_MOVE_TRIED;
  }
  const child = spawnSync(process.execPath, [wrapperPath, ...process.argv.slice(2)], { stdio: 'inherit', env });
  if (child.error) {
    console.error(`[qg] Não foi possível reabrir após a atualização: ${child.error.message}`);
    return 1;
  }
  if (child.signal) {
    process.kill(process.pid, child.signal);
  }
  return child.status ?? 1;
}

// Right after an update the user chose (the wrapper that installed this
// version reopened it with QG_UPDATE_JUST_INSTALLED), an install under the
// legacy name moves to the current name before qg opens. That is how an
// update from a release before the rename lands on the new name in one step.
if (installedUnderLegacyName && process.env.QG_UPDATE_JUST_INSTALLED && !process.env.QG_NPM_MOVE_TRIED) {
  const prefix = npmPrefixFromPackageRoot(packageRoot, ownPackageName);
  if (prefix && ownManifest.version) {
    const moved = moveToCurrentName(ownManifest.version, prefix);
    if (moved.wrapper) {
      process.exit(reopenAfterNpmInstall(process.env.QG_UPDATE_JUST_INSTALLED, moved.wrapper));
    }
    if (!moved.kept) {
      process.exit(1);
    }
  }
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
// Internal to the wrapper's own reopen; never passed on to qg.
delete env.QG_NPM_MOVE_TRIED;
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
  const prefix = npmPrefixFromPackageRoot(packageRoot, ownPackageName);
  if (!prefix) {
    const manual = installedUnderLegacyName
      ? `\n${manualMoveCommands('')}`
      : ` npm install -g ${ownPackageName}@${handoffVersion}`;
    console.error(`[qg] Não foi possível determinar o prefixo npm desta instalação (${packageRoot}); abrindo a versão atual. Para atualizar à mão, rode:${manual}`);
    process.exit(reopenAfterNpmInstall(handoffVersion));
  }
  if (installedUnderLegacyName) {
    // An update of a legacy install is the move to the current name.
    const moved = moveToCurrentName(handoffVersion, prefix);
    if (moved.wrapper) {
      process.exit(reopenAfterNpmInstall(handoffVersion, moved.wrapper));
    }
    process.exit(moved.kept ? reopenAfterNpmInstall(handoffVersion, process.argv[1], true) : 1);
  }
  const installed = installNpmUpdate(handoffVersion, prefix);
  if (!npmSucceeded(installed)) {
    console.error(`[qg] Não foi possível instalar a versão ${handoffVersion} via npm; abrindo a versão atual. Para atualizar à mão, rode: npm install -g ${quotedPrefixFlag(prefix)} ${ownPackageName}@${handoffVersion}`);
  }
  process.exit(reopenAfterNpmInstall(handoffVersion));
}

// A legacy install that did not move (installed by name from an old tutorial
// or shell history, or outside a global prefix) says how, after qg exits so
// the TUI does not hide it. Only on a terminal, and not for update/upgrade,
// which print the steps themselves.
const command = process.argv[2];
if (installedUnderLegacyName && process.stderr.isTTY && command !== 'update' && command !== 'upgrade') {
  console.error(
    `[qg] O qg agora é ${PACKAGE_NAME} no npm, e ${LEGACY_PACKAGE_NAME} não recebe mais versões. ` +
      'Rode `qg upgrade` para trocar; a chave salva e as configurações continuam.',
  );
}

process.exit(child.status ?? 1);
