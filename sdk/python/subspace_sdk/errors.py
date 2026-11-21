from __future__ import annotations

import json
from typing import Any


class SubspaceError(Exception):
    def __init__(
        self,
        code: str,
        message: str,
        *,
        status: int | None = None,
        hint: str | None = None,
        details: Any = None,
        retry_after: float | None = None,
    ) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.status = status
        self.hint = hint
        self.details = details
        self.retry_after = retry_after


class TransportError(SubspaceError):
    def __init__(self, message: str) -> None:
        super().__init__("transport", message)


def error_from_body(status: int, body: bytes, retry_after: float | None = None) -> SubspaceError:
    try:
        payload = json.loads(body) if body else None
    except ValueError:
        payload = None
    err = payload.get("error") if isinstance(payload, dict) else None
    if isinstance(err, dict):
        code = err.get("code")
        message = str(err.get("message") or "")
        if isinstance(code, str):
            return SubspaceError(
                code,
                message,
                status=status,
                hint=err.get("hint"),
                details=err.get("details"),
                retry_after=retry_after,
            )
        if isinstance(code, int):
            return SubspaceError(
                "rpc_error",
                message,
                status=status,
                details={"rpc_code": code},
                retry_after=retry_after,
            )
    text = body.decode("utf-8", "replace").strip()
    return SubspaceError(f"http_{status}", text or f"HTTP {status}", status=status, retry_after=retry_after)
