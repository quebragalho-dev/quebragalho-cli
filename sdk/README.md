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
- Headless runs cannot answer permission prompts. Pre-approve with `auto`,
  `permissionMode`, or restrict tools with `enabledTools` / `disabledTools`.

Protocol changes (a `schemaVersion` bump) must update both SDKs; CI runs both on
every change under `sdk/` or to the protocol doc.
