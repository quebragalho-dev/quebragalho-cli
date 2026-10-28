# QG SDKs

Thin clients that run the `qg` coding agent from your own code. They spawn
`qg exec --output-format stream-json` and turn the
[stream-json protocol](../docs/STREAM_JSON_PROTOCOL.md) into typed events and a
final result. No dependencies, no network code of their own: login, models,
sandboxing and permissions all stay in the `qg` binary.

| Language | Package | Docs |
| --- | --- | --- |
| TypeScript / Node ≥ 18 | `@quebragalho-dev/sdk` | [typescript](typescript/README.md) |
| Python ≥ 3.9 | `quebragalho-sdk` | [python](python/README.md) |

Both need `qg` installed and logged in (`npm i -g @quebragalho-dev/cli`, then
`qg login --device`), or an API key passed to the client.

Design rules shared by both SDKs:

- The prompt goes on stdin and the API key in `QG_API_KEY`, never on the command line.
- A run that does not end in `status: success` raises `QGRunError`; a process that
  dies without `run_end` raises `QGProtocolError`; cancelling raises `QGAbortError`.
- Breaking out of an event loop stops the process.
- An unknown `schemaVersion` fails fast instead of mis-parsing events.
- Headless runs cannot answer permission prompts. Blocked calls are reported in
  `permissionDenials` / `permission_denials`; widen access with `auto` or
  `permissionMode`, or restrict tools with `enabledTools` / `disabledTools`.

Protocol changes (a `schemaVersion` bump) must update both SDKs; CI runs both on
every change under `sdk/` or to the protocol doc.

## Releasing

SDK versions are independent of the CLI's. Both packages must carry the same
version, and the tag must match it.

1. Bump `version` in `sdk/typescript/package.json` (and the lockfile) and `sdk/python/pyproject.toml`, merge to `main`.
2. Push the tag: `git tag sdk-vX.Y.Z && git push origin sdk-vX.Y.Z`.
3. `SDK Publish` verifies, builds, then pauses on the `sdk-publish` environment. Approve in the Actions UI, or:
   `scripts/approve-npm-publish.sh sdk-vX.Y.Z sdk-publish.yml sdk-publish`

Publishing uses OIDC trusted publishing (no stored tokens), with provenance on npm.

One-time setup:

- **PyPI**: add a *pending publisher* at <https://pypi.org/manage/account/publishing/> — project `quebragalho-sdk`, owner `quebragalho-dev`, repository `quebragalho-cli`, workflow `sdk-publish.yml`, environment `sdk-publish`.
- **npm**: the first version of a new package cannot use trusted publishing. Publish it once by hand from `sdk/typescript` (`npm ci && npm run build && npm publish --access public`, with your own npm login and 2FA), then add a trusted publisher on the package's Settings page: repository `quebragalho-dev/quebragalho-cli`, workflow `sdk-publish.yml`, environment `sdk-publish`. Every later release goes through the workflow.
