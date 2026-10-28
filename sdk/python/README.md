# quebragalho-sdk

Run the [Quebra-Galho](https://github.com/quebragalho-dev/quebragalho-cli) (`qg`)
coding agent from Python. Requires Python ≥ 3.9 and the `qg` binary
(`npm i -g @quebragalho-dev/cli`). No dependencies.

```python
from quebragalho import QG

qg = QG(cwd="/path/to/repo")

# One shot
result = qg.run("Explain what this repo does", model="kimi-k3", plan=True)
print(result.text, result.usage.total_tokens)

# Streaming
with qg.stream("Fix the failing test", auto="medium") as run:
    for event in run:
        if event["type"] == "text":
            print(event["delta"], end="")
    done = run.result()  # RunResult

# asyncio
result = await qg.arun("...")
run = await qg.astream("...")
async for event in run: ...
```

## API

- `QG(binary=None, api_key=None, cwd=None, env=None, **defaults)` — `binary` defaults to `$QG_BIN`, then `qg` on PATH. `api_key` is sent as `QG_API_KEY`; omit it to use the stored login. Extra keyword arguments become default run options.
- `qg.run(prompt, **options)` → `RunResult`; raises `QGRunError` on a non-success status. `qg.arun` is the asyncio version.
- `qg.stream(prompt, **options)` → `Run`: iterable of event dicts (as in the protocol doc), plus `result()`, `abort()`, `close()`. `qg.astream` returns an `AsyncRun`.
- `prompt` is a string or `{"text": ..., "images": [{"mediaType": ..., "data": bytes}]}`.
- Options: `model`, `cwd`, `mode`, `plan`, `auto`, `permission_mode`, `max_turns`, `reasoning_effort`, `exec_profile`, `enabled_tools`, `disabled_tools`, `add_dirs`, `resume` (id or `True`), `fork`, `session_title`, `worktree`, `dangerously_skip_permissions`, `extra_args`. Unknown names raise `TypeError`.
- Continue a conversation with `resume=result.session_id`.

Errors: `QGRunError` (`exit_code`, `code`, `result`, `stderr`), `QGProtocolError`, `QGAbortError`, `QGBinaryNotFoundError`, all extending `QGError`.

`usage.cost_brl` / `usage.cost_usd` sum the per-call prices from the QG catalog. They are `None` when no call could be priced (for example, a `qg` older than the field, or a model missing from the catalog).

## Permissions

A headless run cannot answer approval prompts. By default (`auto` mode) sandboxed shell work, including writing files in the workspace, is allowed; actions such as network access are not. The blocked calls come back as `result.permission_denials` (`PermissionDenial`: `id`, `tool`, `reason`, `risk_level`, `categories`), and the model usually explains the failure in `result.text`. To allow more, pass `permission_mode` or `auto="high"` (which enables unsafe tools), or hand the model a narrower task. `enabled_tools` / `disabled_tools` restrict what it can try.
