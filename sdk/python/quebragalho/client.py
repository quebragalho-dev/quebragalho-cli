from __future__ import annotations

import asyncio
import base64
import json
import os
import subprocess
import threading
from typing import Any, AsyncIterator, Dict, Iterator, List, Mapping, Optional, Sequence, Union

from ._acc import Accumulator
from .args import build_args
from .errors import QGAbortError, QGBinaryNotFoundError, QGProtocolError, QGRunError
from .types import SCHEMA_VERSION, Event, RunResult

STDERR_LIMIT = 64 * 1024
KILL_GRACE_S = 5.0

#: A prompt is a string, or {"text": ..., "images": [{"mediaType": ..., "data": bytes|str}]}.
Prompt = Union[str, Mapping[str, Any]]


def encode_prompt(prompt: Prompt) -> str:
    data = {"text": prompt} if isinstance(prompt, str) else dict(prompt)
    message: Dict[str, Any] = {
        "schemaVersion": SCHEMA_VERSION,
        "type": "message",
        "role": "user",
        "content": data["text"],
    }
    images = [
        {
            "mediaType": image["mediaType"],
            "data": image["data"]
            if isinstance(image["data"], str)
            else base64.b64encode(image["data"]).decode("ascii"),
        }
        for image in data.get("images") or []
    ]
    if images:
        message["images"] = images
    return json.dumps(message, ensure_ascii=False) + "\n"


def _fail(acc: Accumulator, stderr: str) -> None:
    result = acc.snapshot()
    if not result.ok:
        err = acc.error or {}
        raise QGRunError(result, err.get("code"), err.get("message"), stderr)


class Run:
    """A live `qg exec` process. Iterate for events, or call result()."""

    def __init__(self, proc: "subprocess.Popen[str]", binary: str) -> None:
        self._proc = proc
        self._binary = binary
        self._acc = Accumulator()
        self._stderr = ""
        self._aborted = False
        self._drained = False
        self._events: Optional[Iterator[Event]] = None
        self._thread = threading.Thread(target=self._drain_stderr, daemon=True)
        self._thread.start()

    def _drain_stderr(self) -> None:
        assert self._proc.stderr is not None
        for chunk in self._proc.stderr:
            self._stderr = (self._stderr + chunk)[-STDERR_LIMIT:]

    @property
    def pid(self) -> int:
        return self._proc.pid

    def abort(self) -> None:
        """Stop the run: terminate, then kill if it lingers."""
        if self._aborted or self._proc.poll() is not None:
            return
        self._aborted = True
        self._proc.terminate()
        timer = threading.Timer(KILL_GRACE_S, self._proc.kill)
        timer.daemon = True
        timer.start()

    def __enter__(self) -> "Run":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def close(self) -> None:
        if not self._drained:
            self.abort()
        self._proc.wait()
        self._thread.join(timeout=1)
        self._close_pipes()

    def _close_pipes(self) -> None:
        for pipe in (self._proc.stdout, self._proc.stderr):
            if pipe is not None:
                pipe.close()

    def __iter__(self) -> Iterator[Event]:
        if self._events is None:
            self._events = self._pump()
        return self._events

    def _pump(self) -> Iterator[Event]:
        assert self._proc.stdout is not None
        try:
            for line in self._proc.stdout:
                line = line.strip()
                if not line:
                    continue
                event = Accumulator.parse(line, self._stderr)
                self._acc.observe(event)
                yield event
            self._drained = True
        finally:
            if not self._drained:
                self.abort()
        self._assert_ended()

    def _assert_ended(self) -> None:
        code = self._proc.wait()
        self._thread.join(timeout=1)
        self._close_pipes()
        if self._acc.end is not None:
            return
        if self._aborted:
            raise QGAbortError()
        raise QGProtocolError(f"qg exited (code {code}) before emitting run_end", self._stderr)

    def result(self) -> RunResult:
        """Drain the stream and return the outcome. Raises QGRunError on a non-success status."""
        for _ in self:
            pass
        self._assert_ended()
        _fail(self._acc, self._stderr)
        return self._acc.snapshot()


class AsyncRun:
    """asyncio variant of Run: `async for` events, or `await run.result()`."""

    def __init__(self, proc: asyncio.subprocess.Process, binary: str) -> None:
        self._proc = proc
        self._binary = binary
        self._acc = Accumulator()
        self._stderr = ""
        self._aborted = False
        self._drained = False
        self._events: Optional[AsyncIterator[Event]] = None
        self._stderr_task = asyncio.ensure_future(self._drain_stderr())

    async def _drain_stderr(self) -> None:
        assert self._proc.stderr is not None
        while True:
            chunk = await self._proc.stderr.read(4096)
            if not chunk:
                return
            self._stderr = (self._stderr + chunk.decode("utf-8", "replace"))[-STDERR_LIMIT:]

    @property
    def pid(self) -> int:
        return self._proc.pid

    def abort(self) -> None:
        if self._aborted or self._proc.returncode is not None:
            return
        self._aborted = True
        self._proc.terminate()
        loop = asyncio.get_running_loop()
        loop.call_later(KILL_GRACE_S, lambda: self._proc.returncode is None and self._proc.kill())

    def __aiter__(self) -> AsyncIterator[Event]:
        if self._events is None:
            self._events = self._pump()
        return self._events

    async def _pump(self) -> AsyncIterator[Event]:
        assert self._proc.stdout is not None
        try:
            while True:
                raw = await self._proc.stdout.readline()
                if not raw:
                    break
                line = raw.decode("utf-8", "replace").strip()
                if not line:
                    continue
                event = Accumulator.parse(line, self._stderr)
                self._acc.observe(event)
                yield event
            self._drained = True
        finally:
            if not self._drained:
                self.abort()
        await self._assert_ended()

    async def _assert_ended(self) -> None:
        code = await self._proc.wait()
        await self._stderr_task
        if self._acc.end is not None:
            return
        if self._aborted:
            raise QGAbortError()
        raise QGProtocolError(f"qg exited (code {code}) before emitting run_end", self._stderr)

    async def result(self) -> RunResult:
        async for _ in self:
            pass
        await self._assert_ended()
        _fail(self._acc, self._stderr)
        return self._acc.snapshot()


class QG:
    """Entry point. Spawns `qg exec` per call; the prompt goes on stdin, the API key in env."""

    def __init__(
        self,
        *,
        binary: Optional[str] = None,
        api_key: Optional[str] = None,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        **defaults: Any,
    ) -> None:
        self._binary = binary or os.environ.get("QG_BIN") or "qg"
        self._api_key = api_key
        self._cwd = cwd
        self._env = dict(env or {})
        self._defaults = defaults
        build_args(**defaults)  # fail fast on typos

    def _prepare(self, options: Dict[str, Any]) -> "tuple[List[str], Optional[str], Dict[str, str]]":
        merged = {**self._defaults, **options}
        if merged.get("cwd") is None:
            merged["cwd"] = self._cwd
        env = {**os.environ, **self._env}
        if self._api_key:
            env["QG_API_KEY"] = self._api_key
        return [self._binary, *build_args(**merged)], merged.get("cwd"), env

    def stream(self, prompt: Prompt, **options: Any) -> Run:
        argv, cwd, env = self._prepare(options)
        try:
            proc = subprocess.Popen(
                argv,
                cwd=cwd,
                env=env,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                encoding="utf-8",
                errors="replace",
            )
        except FileNotFoundError:
            raise QGBinaryNotFoundError(self._binary) from None
        run = Run(proc, self._binary)
        assert proc.stdin is not None
        try:
            proc.stdin.write(encode_prompt(prompt))
            proc.stdin.close()
        except (BrokenPipeError, OSError):
            pass  # process died early; result() reports why
        return run

    def run(self, prompt: Prompt, **options: Any) -> RunResult:
        """Run to completion. Raises QGRunError on a non-success status."""
        with self.stream(prompt, **options) as run:
            return run.result()

    async def astream(self, prompt: Prompt, **options: Any) -> AsyncRun:
        argv, cwd, env = self._prepare(options)
        try:
            proc = await asyncio.create_subprocess_exec(
                *argv,
                cwd=cwd,
                env=env,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
        except FileNotFoundError:
            raise QGBinaryNotFoundError(self._binary) from None
        run = AsyncRun(proc, self._binary)
        assert proc.stdin is not None
        try:
            proc.stdin.write(encode_prompt(prompt).encode("utf-8"))
            await proc.stdin.drain()
            proc.stdin.close()
        except (BrokenPipeError, ConnectionResetError, OSError):
            pass
        return run

    async def arun(self, prompt: Prompt, **options: Any) -> RunResult:
        run = await self.astream(prompt, **options)
        try:
            return await run.result()
        finally:
            run.abort()
