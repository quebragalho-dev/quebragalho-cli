# @quebragalho-dev/sdk

Run the [Quebra-Galho](https://github.com/quebragalho-dev/quebragalho-cli) (`qg`)
coding agent from TypeScript. Requires Node ≥ 18 and the `qg` binary
(`npm i -g @quebragalho-dev/cli`).

```ts
import { QG } from "@quebragalho-dev/sdk";

const qg = new QG({ cwd: "/path/to/repo" });

// One shot
const result = await qg.run("Explain what this repo does", { model: "kimi-k3", plan: true });
console.log(result.text, result.usage.totalTokens);

// Streaming
const run = qg.stream("Fix the failing test", { auto: "medium" });
for await (const event of run) {
  if (event.type === "text") process.stdout.write(event.delta);
  if (event.type === "tool_call") console.error(`> ${event.name}`);
}
const done = await run.result(); // { text, status, usage, changedFiles, sessionId, ... }
```

## API

- `new QG({ binary?, apiKey?, cwd?, env?, defaults? })` — `binary` defaults to `$QG_BIN`, then `qg` on PATH. `apiKey` is sent as `QG_API_KEY`; omit it to use the stored login.
- `qg.run(prompt, options?)` → `Promise<RunResult>`; rejects with `QGRunError` on a non-success status.
- `qg.stream(prompt, options?)` → `Run`: async-iterable of typed `QGEvent`s, plus `result()`, `abort()` and `pid`.
- `prompt` is a string or `{ text, images: [{ mediaType, data }] }`.
- Options: `model`, `cwd`, `mode`, `plan`, `auto`, `permissionMode`, `maxTurns`, `reasoningEffort`, `execProfile`, `enabledTools`, `disabledTools`, `addDirs`, `resume` (id or `true`), `fork`, `sessionTitle`, `worktree`, `dangerouslySkipPermissions`, `extraArgs`, `signal` (`AbortSignal`).
- Continue a conversation with `resume: result.sessionId`.

Errors: `QGRunError` (has `exitCode`, `code`, `result`, `stderr`), `QGProtocolError`, `QGAbortError`, `QGBinaryNotFoundError`, all extending `QGError`.

`usage.costUsd` is set only when the server reports a cost.
