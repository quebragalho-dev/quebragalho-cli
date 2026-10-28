from __future__ import annotations

from typing import TYPE_CHECKING, Optional

if TYPE_CHECKING:
    from .types import RunResult


class QGError(Exception):
    """Base class for every error raised by the SDK."""


class QGBinaryNotFoundError(QGError):
    """The qg binary could not be started."""

    def __init__(self, binary: str) -> None:
        super().__init__(
            f'qg binary not found: "{binary}". Install it (npm i -g @quebragalho-dev/cli) '
            "or pass binary=... / set QG_BIN."
        )
        self.binary = binary


class QGProtocolError(QGError):
    """Output was not valid stream-json, or the process ended without run_end."""

    def __init__(self, message: str, stderr: str = "") -> None:
        super().__init__(f"{message}\n{stderr.strip()}" if stderr.strip() else message)
        self.stderr = stderr


class QGAbortError(QGError):
    """The run was cancelled."""

    def __init__(self) -> None:
        super().__init__("qg run aborted")


class QGRunError(QGError):
    """qg finished but reported a non-success status."""

    def __init__(
        self,
        result: "RunResult",
        code: Optional[str] = None,
        message: Optional[str] = None,
        stderr: str = "",
    ) -> None:
        super().__init__(message or f'qg run ended with status "{result.status}" (exit {result.exit_code})')
        self.result = result
        self.exit_code = result.exit_code
        self.status = result.status
        self.code = code
        self.stderr = stderr
