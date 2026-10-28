from __future__ import annotations

import json
from typing import Dict, List, Optional

from .errors import QGProtocolError
from .types import SCHEMA_VERSION, Event, RunResult, Usage


class Accumulator:
    """Folds stream-json events into a RunResult. Shared by the sync and async runs."""

    def __init__(self) -> None:
        self.run_id = ""
        self.session_id: Optional[str] = None
        self.model: Optional[str] = None
        self.final: Optional[str] = None
        self.text = ""
        self.end: Optional[Dict[str, object]] = None
        self.error: Optional[Dict[str, Optional[str]]] = None
        self.usage = Usage()
        self.changed: List[str] = []
        self.warnings: List[str] = []

    @staticmethod
    def parse(line: str, stderr: str = "") -> Event:
        try:
            event = json.loads(line)
        except ValueError:
            raise QGProtocolError(f"invalid stream-json line: {line[:200]}", stderr) from None
        if not isinstance(event, dict) or event.get("schemaVersion") != SCHEMA_VERSION:
            version = event.get("schemaVersion") if isinstance(event, dict) else None
            raise QGProtocolError(
                f"unsupported schemaVersion {version} (SDK speaks {SCHEMA_VERSION}); "
                "update quebragalho-sdk or qg",
                stderr,
            )
        return event

    def observe(self, event: Event) -> None:
        kind = event.get("type")
        if kind == "run_start":
            self.run_id = event.get("runId", "")
            self.session_id = event.get("sessionId")
            self.model = event.get("model")
        elif kind == "text":
            self.text += event.get("delta", "")
        elif kind == "final":
            self.final = event.get("text", "")
        elif kind == "usage":
            u = self.usage
            u.prompt_tokens += event.get("promptTokens") or 0
            u.completion_tokens += event.get("completionTokens") or 0
            u.cached_input_tokens += event.get("cachedInputTokens") or 0
            u.total_tokens += event.get("totalTokens") or 0
            if event.get("costUsd") is not None:
                u.cost_usd = (u.cost_usd or 0.0) + event["costUsd"]
        elif kind == "tool_result":
            for path in event.get("changedFiles") or []:
                if path not in self.changed:
                    self.changed.append(path)
        elif kind == "warning":
            self.warnings.append(event.get("message", ""))
        elif kind == "error":
            self.error = {"code": event.get("code"), "message": event.get("message")}
        elif kind == "run_end":
            self.end = {"status": event.get("status", "unknown"), "exitCode": event.get("exitCode", -1)}

    def snapshot(self) -> RunResult:
        end = self.end or {}
        return RunResult(
            run_id=self.run_id,
            session_id=self.session_id,
            model=self.model,
            text=self.final if self.final is not None else self.text,
            status=str(end.get("status", "unknown")),
            exit_code=int(end.get("exitCode", -1)),  # type: ignore[call-overload]
            usage=self.usage,
            changed_files=list(self.changed),
            warnings=list(self.warnings),
        )
