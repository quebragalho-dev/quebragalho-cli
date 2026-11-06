# npm packaging

How `@quebragalho/cli` is put together on npm, why it is shaped this way, and the
rules the release pipeline must follow. Read this before touching
`package.json`, `bin/qg.js`, `scripts/postinstall.mjs`,
`scripts/npm/build-platform-packages.mjs`, or the npm-publish steps of
`release-artifacts.yml`.

## Goals

A `npm install -g @quebragalho/cli` must be **silent and self-contained**:

- No `EBADENGINE` warnings — ours or from any package in the dependency tree.
- No install scripts anywhere in the tree, so npm's `allow-scripts` gating,
  Bun's blocked-by-default lifecycle scripts, and pnpm's strict mode all
  install qg without prompts, trust ceremonies, or broken binaries.
- No network fetches outside the npm registry at install time. GitHub being
  down or rate-limited must not break `npm install`.
- Browser control (`agent-browser`) and terminal control (`tuistory`) work out
  of the box.

## Architecture

The model is the one used by Codex, esbuild, and Biome: a tiny wrapper package
plus per-platform payloads carrying the native binaries.

```
@quebragalho/cli                    <- wrapper: bin/qg.js + optionalDependencies
├─ @quebragalho/cli-darwin-arm64 -> npm:@quebragalho/cli@{version}-darwin-arm64
├─ @quebragalho/cli-darwin-x64   -> npm:@quebragalho/cli@{version}-darwin-x64
├─ @quebragalho/cli-linux-arm64  -> npm:@quebragalho/cli@{version}-linux-arm64
├─ @quebragalho/cli-linux-x64    -> npm:@quebragalho/cli@{version}-linux-x64
├─ @quebragalho/cli-win32-x64    -> npm:@quebragalho/cli@{version}-win32-x64
└─ @quebragalho/cli-win32-arm64  -> npm:@quebragalho/cli@{version}-win32-arm64
```

- The platform "packages" are **versions of the same `@quebragalho/cli` package**,
  published at suffixed versions (`0.4.0-linux-x64`) and referenced through
  `npm:` aliases in `optionalDependencies`. One package name means one npm
  trusted-publisher configuration — no new publish credentials per platform.
  The alias suffixes use Node's `process.platform`/`process.arch` names so
  `bin/qg.js` can derive its platform package directly.
- Each platform version sets `os` and `cpu`, so npm installs exactly one of
  them and skips the rest.
- A platform payload is assembled from the platform's release archive by
  `scripts/npm/build-platform-packages.mjs` and contains the `qg` binary,
  the platform's sandbox helpers, and the vendored `helpers/` tree (see
  below). It has no `bin`, no scripts, and no dependencies — it is inert on
  its own; the wrapper execs the binary out of it.
- The published wrapper has **no scripts and no dependencies** — the
  assembly script strips both from the repo `package.json` and injects the
  exact-version `optionalDependencies`. (The repo `package.json` keeps
  `agent-browser`/`tuistory` in `dependencies` purely as the version pins for
  the vendored helpers tree; they are never installed by consumers.)
- There is **no windows-arm64 agent-browser helper binary** (upstream ships no
  win32-arm64 build); the win32-arm64 payload stages the x64 helper under the
  arm64 name the launcher resolves, so it runs under emulation on Windows on
  ARM while `qg.exe` itself is native.

### Binary resolution in `bin/qg.js`

1. Resolve `@quebragalho/cli-<platform>-<arch>` and exec the `qg` binary from
   it. This wins over any previously downloaded copy — the platform version is
   pinned to the wrapper release.
2. Fall back to a binary previously downloaded next to the wrapper.
3. If neither exists (`--omit=optional`, package managers that skip optional
   dependencies), run the **fallback downloader**: `scripts/postinstall.mjs`,
   the exact logic that used to run as a postinstall hook (HTTPS-only, SHA-256
   verified against the release's own checksum file, no zip-slip), invoked by
   the wrapper itself. Failures are deliberately not cached — the fetch
   retries on every run until a binary is in place, so a transient network
   error self-heals. Platforms outside the matrix above skip the attempt entirely rather
   than probing the network each run.
4. If the download is impossible too, print build-from-source guidance.

There is deliberately **no `scripts.postinstall`** in any published
`package.json`. The downloader exists only as a first-run fallback.

### Vendored helpers (`helpers/`)

`agent-browser` (Apache-2.0, vercel-labs/agent-browser) and `tuistory` are
**vendored binaries/packages inside the platform payload**, not npm
dependencies of the wrapper:

- The repo pins `agent-browser` below the versions that declare
  `engines: { node: ">=24", pnpm: ">=11" }` (introduced upstream in 0.28.0),
  so even a direct `npm install` of the repo stays `EBADENGINE`-free on the
  supported Node `>=18` range. As a vendored tree it produces no warnings at
  all, because npm never resolves it.
- `qg-release package` already stages the helpers tree into every release
  archive (`stageLocalControlHelpers` runs `npm ci` from the repo's
  `package.json` pins + lockfile on the native builder). The assembly script
  reuses that staged tree, so npm installs and `install.sh` installs get
  identical helpers.
- The Go binary resolves helpers from `<binary dir>/helpers/node_modules/.bin`
  on its own (`internal/localcontrol/browser.go`, `adjacentHelper`) — no
  wrapper involvement, no configuration.
- **Symlink materialization:** `npm pack` silently drops symlinks, and npm's
  own `.bin` shims are symlinks on POSIX. The assembly script rewrites every
  `.bin` symlink into a relocatable `#!/bin/sh` exec shim and dereferences any
  other symlink, so the vendored tree survives publishing. (Verified
  empirically; nested `node_modules` under `helpers/` IS packed — only the
  package-root `node_modules` is always ignored.)
- **Binary pruning:** agent-browser ships one ~11 MB native binary per
  platform (7 total). The assembly script keeps only the payload's own
  platform binary (plus the musl variant on linux, which its launcher detects
  at runtime) — about 65 MB saved per platform package.

## Publishing rules (release pipeline)

These are the invariants `release-artifacts.yml` must hold. Breaking the first
one is user-visible immediately.

1. **Platform versions must never become `latest`.** `0.4.0-linux-x64` is a
   semver *prerelease* of `0.4.0`; publishing it without an explicit
   non-`latest` dist-tag would clobber `latest` and users would install a
   platform payload as the CLI. The workflow publishes platform versions with
   `--tag platform` and asserts `latest` survived before the wrapper publish,
   then asserts `latest` equals the wrapper version after it. Both lookups
   tolerate a failed `npm view` (registry lag, or no `latest` tag yet on a
   bootstrap publish) — a missing value must not abort the job between the
   platform and wrapper publishes, which would strand a half-published
   release.
2. **Platform versions publish before the wrapper.** The wrapper's
   `optionalDependencies` pin exact suffixed versions; publishing the wrapper
   first would create a window where installs resolve aliases that 404.
3. **Exact-version pinning.** The wrapper at `X.Y.Z` references platform
   versions `X.Y.Z-<platform>-<arch>` exactly — never ranges — so a wrapper
   and its binaries can never skew.
4. All publishes go through the existing npm OIDC trusted-publishing flow and
   the `npm-publish` environment gate; no tokens. Because the platform
   payloads are versions of the same package, they reuse the single
   trusted-publisher configuration.

`scripts/npm/publish-packages.sh` holds these rules. It reads the package name
from the assembled wrapper, refuses to start when the wrapper's aliases do not
match the platform payloads beside it, and skips versions already on the
registry, so re-running a job that failed halfway is safe.
`internal/npmwrapper/publish_test.go` runs it against a fake registry.

The name lives in `package.json` only. `build-platform-packages.mjs` names the
wrapper, the platform versions and the aliases from it (or from
`--package-name`), `bin/qg.js` reads the name it was installed under from the
`package.json` beside it, and the Go constant `npmPackageName` is held equal
to `package.json` by a test.

`qg update` detects an npm install by finding a `package.json` named
`@quebragalho/cli` next to the running binary — true inside a platform payload
too — and updates via `npm install -g @quebragalho/cli@latest`.

## The rename from @quebragalho-dev/cli

Up to 0.0.15 the package was `@quebragalho-dev/cli`. It moved to
`@quebragalho/cli`, under the product's npm org. Deprecating the old name alone
would strand every existing install: qg 0.0.15 checks and updates
`@quebragalho-dev/cli`, so with nothing newer there it never sees an update,
and npm only shows a deprecation message when it installs something.

So one release (0.0.16) also goes out under the old name, as a **bridge**:

- The publish job assembles the same verified artifacts a second time with
  `build-platform-packages.mjs --package-name @quebragalho-dev/cli` and
  publishes them with `publish-packages.sh`, after the `@quebragalho/cli`
  publish. A gate runs it only while the old name's latest is still 0.0.15 (or
  is this version, on a re-run), so it happens once.
- qg 0.0.15 then updates to the bridge as usual. Its wrapper reopens the
  bridge with `QG_UPDATE_JUST_INSTALLED`, and the bridge wrapper, reading the
  old name in its own `package.json`, moves the install before its binary
  runs: `npm uninstall -g --prefix <prefix> @quebragalho-dev/cli`, then
  `npm install -g --prefix <prefix> @quebragalho/cli@<version>`, then it
  reopens the same command line from the new install. The old package goes
  first because both own the `qg` bin: npm refuses the second install with
  EEXIST, and a forced install loses the bin when the old package is removed
  later.
- If the new install fails, the wrapper reinstalls the old name at its
  version, so qg keeps working, and prints the two commands. The reopen then
  carries `QG_NPM_MOVE_TRIED` (internal to the wrapper, never passed to qg) so
  it is not tried twice in a row.
- An install that did not move (installed by name from an old tutorial, or
  outside a global prefix) hears about it after each run, on a terminal.
  `qg upgrade` hands the move to the wrapper whether or not a newer version is
  out, and `qg update --check` prints the steps. Run outside the wrapper, `qg
  upgrade` refuses with the two commands instead of removing the package that
  holds the running binary.

### Runbook

1. **Reserve the new name** (once, by an owner of the `quebragalho` npm org).
   npm only lets a trusted publisher be configured on a package that exists,
   so publish a placeholder by hand. It is a prerelease, so it needs a tag of
   its own:

   ```bash
   mkdir qg-bootstrap && cd qg-bootstrap
   cat > package.json <<'JSON'
   {
     "name": "@quebragalho/cli",
     "version": "0.0.0-bootstrap.0",
     "description": "Reserva do nome. O qg sai aqui a partir da 0.0.16.",
     "license": "MIT",
     "repository": { "type": "git", "url": "git+https://github.com/quebragalho-dev/quebragalho-cli.git" }
   }
   JSON
   npm publish --access public --tag bootstrap
   ```

   If the registry points `latest` at the placeholder, that is fine: the
   workflow's guard only rejects a `latest` on a platform version, and the
   0.0.16 wrapper publish takes `latest`.
2. **Trust the release workflow** on npmjs.com, in the settings of
   `@quebragalho/cli`: publisher GitHub Actions, organization
   `quebragalho-dev`, repository `quebragalho-cli`, workflow
   `release-artifacts.yml`, environment `npm-publish`. The old package already
   trusts the same workflow (0.0.15 went out through it), which the bridge
   publish uses.
3. **Release 0.0.16** the usual way (release PR, `v0.0.16` tag, approve the
   `npm-publish` environment). The job publishes `@quebragalho/cli`, then the
   bridge under `@quebragalho-dev/cli`.
4. **Check:** `npm view @quebragalho/cli dist-tags` shows `latest: 0.0.16` and
   `npm view @quebragalho-dev/cli dist-tags` shows `latest: 0.0.16`; a clean
   `npm install -g @quebragalho/cli` runs `qg --version`; an install of
   `@quebragalho-dev/cli@0.0.15` that runs `qg upgrade` ends under
   `@quebragalho/cli`.
5. **Deprecate the old name** (by an owner of `@quebragalho-dev/cli`):

   ```bash
   npm deprecate @quebragalho-dev/cli "O qg agora é @quebragalho/cli: npm uninstall -g @quebragalho-dev/cli && npm install -g @quebragalho/cli"
   ```

   Drop the placeholder too: `npm unpublish @quebragalho/cli@0.0.0-bootstrap.0`
   within 72 hours of publishing it, or `npm deprecate` that version after.
6. **Delete the two bridge steps** from `release-artifacts.yml` in the next
   release, and revoke the old package's trusted publisher. The wrapper and Go
   recognition of the old name stay: the bridge stays the old name's `latest`,
   so whoever installs it later still gets moved.

## Runbook: bumping the vendored helpers

The vendored helper versions are pinned by the repo `package.json`
`dependencies` + `package-lock.json`; the release build vendors exactly what
the lockfile resolves. `agent-browser` carries no range at all (exact
`0.27.0`), so no `npm install` can float it past the `engines: node >=24`
floor; other helpers stay `^`-pinned to the lockfile.

- Routine bump: `npm install agent-browser@<version>` (or `tuistory@…`) in the
  repo to update pin + lockfile, verify the browser/terminal tools end-to-end,
  ship with the next release. Keep `agent-browser` below the upstream versions
  that declare `engines: node >=24` (first seen in 0.28.0): anything higher
  reintroduces `EBADENGINE` for users on the supported Node `>=18` range.
- Security response: if a vendored helper publishes a security fix, the bump
  is release-worthy on its own — cut a patch release of qg carrying only the
  pin change.
- Attribution: agent-browser is Apache-2.0; its LICENSE ships inside the
  vendored package directory (`helpers/node_modules/agent-browser/`), which
  satisfies redistribution attribution.

## History / rationale

Until v0.3.x the npm package was a wrapper with a `postinstall` downloader and
`agent-browser`/`tuistory` as regular dependencies. That produced three
warnings on every install (`EBADENGINE` from agent-browser's `node >=24`
engines pin, and `allow-scripts` warnings for both postinstall scripts), broke
silently under Bun's default script blocking, and coupled `npm install` to
GitHub Releases availability. The platform-package model removes the warnings
structurally — there is nothing left in the tree for a package manager to warn
about — rather than asking users to approve or suppress them.

## Approving a publish from the terminal

The `npm-publish` environment has a required reviewer, so the publish job waits
after the binaries are packaged. Approve it without opening the Actions page:

```bash
scripts/approve-npm-publish.sh vX.Y.Z
```

It uses your own `gh` login and the same API the "Approve and deploy" button
calls, so only a required reviewer can run it.
