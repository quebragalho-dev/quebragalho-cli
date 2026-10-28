from __future__ import annotations

from typing import Any, List, Optional, Sequence, Union

_SCALAR = {
    "model": "--model",
    "cwd": "--cwd",
    "mode": "--mode",
    "auto": "--auto",
    "permission_mode": "--permission-mode",
    "max_turns": "--max-turns",
    "reasoning_effort": "--reasoning-effort",
    "exec_profile": "--exec-profile",
    "fork": "--fork",
    "session_title": "--session-title",
}
_KNOWN = set(_SCALAR) | {
    "plan",
    "enabled_tools",
    "disabled_tools",
    "add_dirs",
    "resume",
    "worktree",
    "dangerously_skip_permissions",
    "extra_args",
}


def build_args(**opts: Any) -> List[str]:
    """Translate keyword options into `qg exec` flags. The prompt travels on stdin, never argv."""
    unknown = set(opts) - _KNOWN
    if unknown:
        raise TypeError(f"unknown qg option(s): {', '.join(sorted(unknown))}")
    args = ["exec", "--input-format", "stream-json", "--output-format", "stream-json"]
    for key, flag in _SCALAR.items():
        value = opts.get(key)
        if value is not None and value != "":
            args += [flag, str(value)]
    if opts.get("plan"):
        args.append("--plan")
    for key, flag in (("enabled_tools", "--enabled-tools"), ("disabled_tools", "--disabled-tools")):
        tools: Optional[Sequence[str]] = opts.get(key)
        if tools:
            args += [flag, ",".join(tools)]
    for directory in opts.get("add_dirs") or []:
        args += ["--add-dir", str(directory)]
    resume: Union[str, bool, None] = opts.get("resume")
    if resume is True:
        args.append("--resume")
    elif resume:
        args += ["--resume", str(resume)]
    worktree: Union[str, bool, None] = opts.get("worktree")
    if worktree is True:
        args.append("--worktree")
    elif worktree:
        args += ["--worktree", str(worktree)]
    if opts.get("dangerously_skip_permissions"):
        args.append("--dangerously-skip-permissions")
    args += list(opts.get("extra_args") or [])
    return args
