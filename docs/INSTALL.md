# Installing QG

Pick whichever fits how you already install things. None of these is the
blessed one.

| Method | Command | Self-update |
| --- | --- | --- |
| Install script (Linux, macOS) | `curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh \| bash` | `qg upgrade` |
| Install script (Windows) | `irm https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.ps1 \| iex` | `qg upgrade` |
| npm | `npm install -g @quebragalho-dev/qg` | `qg upgrade` (runs npm) |
| Release archive | download from [Releases](https://github.com/quebragalho-dev/quebragalho-cli/releases) | `qg upgrade` |
| mise | `mise use -g ubi:quebragalho-dev/quebragalho-cli` | `mise upgrade` |
| `go install` | `go install github.com/quebragalho-dev/quebragalho-cli/cmd/qg@latest` | rerun the command |
| Source | `go build -o qg ./cmd/qg` | rebuild |

`qg upgrade` follows whatever owns the binary, with one exception it cannot
detect. It delegates to npm for an npm install, refuses outright for a Homebrew
keg and names `brew upgrade qg`, and replaces the binary in place for
everything else — including a mise-managed one, which is the exception.

mise is the one case it cannot detect: a mise-managed binary is an ordinary
standalone install on disk, so `qg upgrade` will replace it and leave mise
describing a version that is no longer there. Use `mise upgrade` instead.

Release archives are the substrate for most of the above: the install scripts
and the npm fallback both download a platform archive from a published GitHub
Release and verify its checksum.

## npm

```bash
npm install -g @quebragalho-dev/qg
qg
```

The package supports Linux and macOS on x64 and arm64, and Windows on x64
(Windows on ARM runs the x64 build under emulation). It installs a small
`qg` wrapper plus, as an optional dependency, a platform payload with the
native binary and the bundled browser/terminal control helpers. There are no
install scripts and nothing is downloaded from outside the npm registry, so
the install is silent and works identically under npm, Bun, pnpm, and yarn —
no trust or approval steps. See [NPM_PACKAGING.md](NPM_PACKAGING.md) for the
package architecture.

Requirements:

- Node.js 18+
- network access to npm

If the install skipped optional dependencies (`npm install --omit=optional`,
or a package manager configured to do so), the wrapper falls back to
downloading the binary from the matching GitHub Release, with checksum
verification. The fetch is retried on each run until a binary is in place (a
transient network failure heals itself; an install directory the current user
cannot write to keeps failing — rerun the install or the command below with
sufficient permissions). To trigger the fetch manually:

```bash
node "$(npm root -g)/@quebragalho-dev/qg/scripts/postinstall.mjs"
```

## Linux And macOS Script

Install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh | bash
```

From a checkout:

```bash
scripts/install.sh
```

Install a specific version:

```bash
QG_VERSION=0.1.0 scripts/install.sh
scripts/install.sh --version 0.1.0
```

Install somewhere else:

```bash
QG_INSTALL_DIR="$HOME/bin" scripts/install.sh
scripts/install.sh --install-dir "$HOME/bin"
```

Defaults:

- Repository: `quebragalho-dev/quebragalho-cli`
- Version: latest GitHub release
- Install path: `~/.local/bin/qg`

Requirements: Bash, `curl` or `wget`, `tar`, and `shasum` or `sha256sum`.

## Windows PowerShell Script

Install the latest release:

```powershell
irm https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.ps1 | iex
```

From a checkout:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1
```

Install a specific version:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version 0.1.0
```

Install somewhere else:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -InstallDir "$env:USERPROFILE\bin"
```

Defaults:

- Repository: `quebragalho-dev/quebragalho-cli`
- Version: latest GitHub release
- Install path: `%LOCALAPPDATA%\qg\bin\qg.exe`

## mise

[mise](https://mise.jdx.dev/) installs QG straight from the GitHub Release
archives through its `ubi` backend, so there is nothing extra to publish and no
registry in the middle:

```bash
mise use -g ubi:quebragalho-dev/quebragalho-cli
```

Pin a version the same way you would any other tool:

```bash
mise use -g ubi:quebragalho-dev/quebragalho-cli@0.7.1
```

Updates come from `mise upgrade`. `qg upgrade` also works, because a
mise-managed binary is an ordinary standalone install, but then mise's records
describe a version that is no longer on disk. Prefer `mise upgrade`.

## go install

```bash
go install github.com/quebragalho-dev/quebragalho-cli/cmd/qg@latest
```

This builds from source, so it needs Go 1.26.6+ and it does not go through the
release archives. Two consequences worth knowing before you pick it:

- On Linux you also need the sandbox helper, which is a separate binary and is
  not installed by this command. See
  [Sandbox Helpers For Source Builds](#sandbox-helpers-for-source-builds).
  Without it, native sandboxing is unavailable.
- `qg upgrade` treats the result as a standalone install and will replace the
  binary with a release build rather than rebuilding from source. If you chose
  `go install` deliberately, rerun it instead.

macOS and Windows need no extra helper.

## From Source

```bash
git clone https://github.com/quebragalho-dev/quebragalho-cli.git
cd qg
go run ./cmd/qg
```

Build a local binary:

```bash
go build -o qg ./cmd/qg
```

Source builds require Go 1.26.6+.

### Sandbox Helpers For Source Builds

Release archives include the platform sandbox helpers. If you build directly
from source, build the helpers you need:

Linux:

```bash
go build -o qg ./cmd/qg
go build -o qg-linux-sandbox ./cmd/qg-linux-sandbox
go build -o qg-seccomp ./cmd/qg-seccomp
```

Put `qg` and `qg-linux-sandbox` in the same directory on `PATH`, for example
`~/.local/bin`. `qg-seccomp` is kept as a compatibility wrapper; the sandbox
helper applies the Unix-socket filter itself when that sandbox option is enabled.
Linux native sandboxing also requires Bubblewrap to be installed.

macOS uses the system sandbox and does not need an extra helper binary.

### Termux (Android)

QG can run natively on Android via [Termux](https://termux.dev/). Build with
`GOOS=android` to avoid the `faccessat2` syscall that is blocked by Samsung's
seccomp filter on Android:

```bash
# Install Go in Termux
pkg install golang

# Build QG for Android
git clone https://github.com/quebragalho-dev/quebragalho-cli.git
cd qg
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -ldflags="-s -w" -o qg ./cmd/qg

# Move into PATH
mv qg ~/.local/bin/
```

> **Why `GOOS=android`?** Go 1.26+ detects `runtime.GOOS == "android"` and skips
> the `faccessat2` syscall inside `os/exec.findExecutable`, falling back to
> permission-bit checks. Without this flag, Android's seccomp sends SIGSYS and
> kills the process whenever QG looks up a binary on `PATH` (git, sh, etc.).

**DNS.** Android does not expose `/etc/resolv.conf`. Go's pure-Go DNS resolver
needs one. Use `proot` to bind-mount Termux's resolver config:

```bash
pkg install proot
proot -b "$PREFIX/etc/resolv.conf:/etc/resolv.conf" qg
```

Create a wrapper at `~/.local/bin/qg` to avoid typing proot every time:

```bash
#!/data/data/com.termux/files/usr/bin/bash
exec proot -b "$PREFIX/etc/resolv.conf:/etc/resolv.conf" ~/.local/bin/qg.bin "$@"
```

**Scroll.** On native Termux (not under PRoot), mouse scrolling works out of the
box. The TUI uses Bubble Tea's `AllMotion` mouse mode by default. If you run QG
inside PRoot (e.g. through proot-distro), the scroll fix activates `CellMotion`
to avoid PRoot's ptrace interference with the 1003 escape sequence.

**Providers.** QG uses a single provider (the Quebra Galho gateway) on
Termux. Set its API key before running:

```bash
export QUEBRAGALHO_API_KEY=sk-...
```

Windows source builds can use the main `qg.exe` as the command runner and setup
helper through QG's built-in self-dispatch path. If you want a release-style
layout anyway, build the standalone helper executables next to `qg.exe`:

```powershell
go build -o qg.exe ./cmd/qg
go build -o qg-windows-command-runner.exe ./cmd/qg-windows-command-runner
go build -o qg-windows-sandbox-setup.exe ./cmd/qg-windows-sandbox-setup
```

## Release Archive Format

Release archives are named:

- `qg-v<version>-linux-<arch>.tar.gz`
- `qg-v<version>-macos-<arch>.tar.gz`
- `qg-v<version>-windows-<arch>.zip`

Supported targets:

- `linux-x64`
- `linux-arm64`
- `macos-x64`
- `macos-arm64`
- `windows-x64` (Windows on ARM runs this build under emulation)

Each archive must have a matching `.sha256` file. The install scripts download
both files, verify the checksum, and then copy the binary into the install
directory.

## Updating

Check for a newer release, then download, verify, and install it in place:

```bash
qg update --check
qg upgrade
```

See the [update guide](UPDATE.md) for update modes, flags, and platform details.

`qg upgrade` behaves differently depending on how QG was installed, because
overwriting a binary a package manager owns leaves that manager describing a
version that is no longer there:

- **npm**: runs `npm install -g @quebragalho-dev/qg@latest` for you.
- **Homebrew**: refuses, and tells you to run `brew upgrade qg`. Replacing the
  keg binary directly would be reverted by the next `brew upgrade` or
  `brew reinstall`.
- **everything else**: downloads the verified release archive and replaces the
  binary in place.

Homebrew is detected by the binary living inside a Cellar keg that carries
Homebrew's own `INSTALL_RECEIPT.json`. The keg shape alone is not enough: a
directory tree you built yourself can have exactly that shape, and refusing to
update an install Homebrew never touched would be a real cost for a made-up
reason. The receipt is the thing only Homebrew writes.

An ordinary install under `/usr/local/bin` is left alone even on an Intel Mac
where that is also the Homebrew prefix.
