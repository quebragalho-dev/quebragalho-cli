# QG Environment Variables

Reference for the `QG_*` variables the code reads that have no dedicated
section elsewhere. (`QG_NOTIFY_WEBHOOK_URL` and `QG_NOTIFY_WEBHOOK_SUMMARY`
are covered in `docs/GITHUB_ACTION.md`; `QG_THEME` in `docs/THEMES.md`;
`QG_NO_UPDATE_CHECK` in `SECURITY.md`.) Values below come from the readers
cited per row; unset means the default applies.

| Variable | What it does | Values | Default |
| --- | --- | --- | --- |
| `QG_MAX_TURNS` | Caps the per-run tool-turn budget (`internal/config/resolver.go`). | Positive integer, clamped to `500` (`MaxTurnsCeiling`); unparsable values are ignored. | Config `maxTurns`. |
| `QG_CHECKPOINTS` | Toggles checkpoint capture of before-mutation file state (`internal/sessions/checkpoint.go`). | `off` disables; anything else leaves it on. | On. |
| `QG_CHECKPOINT_MAX_BYTES` | Max checkpoint blob size (`internal/sessions/checkpoint.go`). | Positive integer bytes; invalid values are ignored. | `5242880` (5 MiB). |
| `QG_COMPACTION_MODEL` | Model used for compaction summarization; `main` forces the session's main model (`internal/providers/compaction_model.go`). | Model id, `main`, or empty. Env wins over `preferences.compactionModel`. | Main model. |
| `QG_FORMAT_ON_WRITE` | Auto-runs the file's formatter after write/edit tools (`internal/tools/format_on_write.go`). | Set to anything but `0`/`false` to enable. | Off. |
| `QG_MOUSE_MODE` | Forces the TUI mouse reporting mode (`internal/tui/mouse.go`). | `all`, `cell`, `off`/`none`; unrecognized values fall back to auto-detect. | Auto-detect. |
| `QG_REDUCED_MOTION` | Replaces streaming fade and animated spinner with static equivalents (`internal/tui/streaming_fade.go`). | Set to anything but `0`/`false` to enable. | Off (forced on without a TTY). |
| `QG_NO_STREAM_CLEAR` | Disables the fast incremental streaming redraw path (`internal/tui/model.go`). | Set to anything but `0`/`false` to disable. | Fast path when supported. |
| `QG_STREAM_IDLE_TIMEOUT` | Watchdog for idle model streams (`internal/providers/providerio/providerio.go`). | Go duration (`90s`) or bare seconds (`300`); `0`/`off`/`none`/`disabled` turns the watchdog off; unparsable falls back to default. | `5m`. |
| `QG_TOOL_OUTPUT_CEILING_TOKENS` | Per-tool-call output ceiling in estimated tokens (bytes/4); overflow spills to disk, re-readable via grep/read (`internal/tools/output_ceiling.go`). | Integer tokens; `0` or negative disables the ceiling; unparsable keeps the default. | `16000`. |
| `QG_WEBSEARCH_BASE_URL` | Enables the `web_search` tool; unset reports it as unconfigured (`internal/tools/web_search.go`). | URL of a generic JSON search backend (`POST {query,limit}`) or a SearXNG instance. | Unset (tool disabled). |
| `QG_WEBSEARCH_API_KEY` | Bearer token sent as `Authorization: Bearer` to the search backend (`internal/tools/web_search.go`). | Token string; may be empty for keyless backends. | Empty. |
| `QG_WEBSEARCH_PROVIDER` | Forwarded as `provider` in the search request body so an aggregating endpoint can route the query; `searxng` selects the SearXNG GET shape instead (`internal/tools/web_search.go`). | Provider name, e.g. `searxng`. | Empty. |
| `QG_TRACE` | Writes a per-turn trace snapshot for the run (`internal/cli/exec.go`). | File path, or `-` for stderr. | Off. |
| `QG_DISABLE_PROMPT_CACHE_KEY` | Omits the prompt cache key from provider requests (`internal/providers/openai/provider.go`). | Set to anything but `0`/`false` to omit. | Key sent. |
| `QG_MCP_PERMISSIONS_PATH` | Overrides where MCP server permissions are stored (`internal/mcp/permissions.go`). | Absolute path, or relative (resolved against cwd). | Under the user config dir. |
| `QG_SANDBOX_GRANTS_PATH` | Overrides where sandbox command grants are stored (`internal/sandbox/grants.go`). | Absolute path, or relative (resolved against cwd). | Under the user config dir. |
| `QG_DAEMON_REMOTE_TOKEN` | Bearer token the daemon bridge accepts (`internal/daemon/remote/auth.go`). | Non-empty secret; never logged. | Unset (bridge refuses clients). |
| `QG_DAEMON_REMOTE_TOKEN_FILE` | File holding the bridge bearer token, used when the token itself is unset (`internal/daemon/remote/auth.go`). | Path to a file whose trimmed contents are the secret. | Unset. |
| `QG_CRED_STORAGE` | Selects the API-key credential backend (`internal/credstore/credstore.go`). | `keyring`, `encrypted-file`, or `file` (plaintext opt-out). | Auto: `keyring` on macOS, `encrypted-file` elsewhere. |
