import type { RunOptions } from "./types.ts";

/** Translate RunOptions into `qg exec` flags. The prompt travels on stdin, never argv. */
export function buildArgs(opts: RunOptions): string[] {
  const args = ["exec", "--input-format", "stream-json", "--output-format", "stream-json"];
  const flag = (name: string, value: string | number | undefined) => {
    if (value !== undefined && value !== "") args.push(name, String(value));
  };
  flag("--model", opts.model);
  flag("--cwd", opts.cwd);
  flag("--mode", opts.mode);
  if (opts.plan) args.push("--plan");
  flag("--auto", opts.auto);
  flag("--permission-mode", opts.permissionMode);
  flag("--max-turns", opts.maxTurns);
  flag("--reasoning-effort", opts.reasoningEffort);
  flag("--exec-profile", opts.execProfile);
  if (opts.enabledTools?.length) flag("--enabled-tools", opts.enabledTools.join(","));
  if (opts.disabledTools?.length) flag("--disabled-tools", opts.disabledTools.join(","));
  for (const dir of opts.addDirs ?? []) flag("--add-dir", dir);
  if (opts.resume === true) args.push("--resume");
  else flag("--resume", opts.resume);
  flag("--fork", opts.fork);
  flag("--session-title", opts.sessionTitle);
  if (opts.worktree === true) args.push("--worktree");
  else flag("--worktree", opts.worktree);
  if (opts.dangerouslySkipPermissions) args.push("--dangerously-skip-permissions");
  args.push(...(opts.extraArgs ?? []));
  return args;
}
