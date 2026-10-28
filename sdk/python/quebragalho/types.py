from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

#: Stream-JSON schema version this SDK understands (docs/STREAM_JSON_PROTOCOL.md).
SCHEMA_VERSION = 2

#: Events are plain dicts exactly as emitted by `qg exec -o stream-json`.
Event = Dict[str, Any]


@dataclass
class Usage:
    prompt_tokens: int = 0
    completion_tokens: int = 0
    cached_input_tokens: int = 0
    total_tokens: int = 0
    #: Run cost from the QG catalog pricing; None when no call could be priced.
    cost_usd: Optional[float] = None
    cost_brl: Optional[float] = None


@dataclass
class PermissionDenial:
    """A tool call that needed approval, which a headless run cannot give."""

    id: str
    tool: str
    reason: Optional[str] = None
    risk_level: Optional[str] = None
    categories: List[str] = field(default_factory=list)


@dataclass
class RunResult:
    run_id: str
    text: str
    status: str
    exit_code: int
    session_id: Optional[str] = None
    model: Optional[str] = None
    usage: Usage = field(default_factory=Usage)
    changed_files: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)
    #: Calls blocked for lack of approval. Re-run with permission_mode/auto, or narrow the task.
    permission_denials: List[PermissionDenial] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return self.status == "success" and self.exit_code == 0
