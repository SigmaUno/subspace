from __future__ import annotations

import json
import time
import urllib.request
import uuid
from collections.abc import Callable
from typing import Any

from ._transport import HTTPResult, backoff, parse_retry_after, send
from .errors import SubspaceError, TransportError, error_from_body

PROTOCOL_VERSION = "2025-06-18"

IDEMPOTENT_TOOLS = frozenset(
    {
        "claim_task",
        "release_task",
        "submit",
        "review",
        "update_status",
        "report_tests",
        "ask_owner",
        "post_message",
        "add_note",
        "assign",
        "reassign",
        "propose_plan",
        "create_task",
        "create_task_from_template",
        "request_spawn",
    }
)


class MCPClient:
    def __init__(
        self,
        url: str,
        token: str,
        *,
        timeout: float = 30.0,
        retries: int = 3,
        sleep: Callable[[float], None] = time.sleep,
        client_name: str = "subspace-sdk",
    ) -> None:
        self._url = url
        self._token = token
        self._timeout = timeout
        self._retries = retries
        self._sleep = sleep
        self._client_name = client_name
        self._session: str | None = None
        self._initialized = False
        self._request_id = 0

    def __enter__(self) -> MCPClient:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def initialize(self) -> dict[str, Any]:
        result = self._rpc(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": self._client_name, "version": "0.1.0"},
            },
        )
        self._notify("notifications/initialized")
        self._initialized = True
        return result

    def close(self) -> None:
        session, self._session, self._initialized = self._session, None, False
        if session is None:
            return
        headers = self._headers()
        headers["Mcp-Session-Id"] = session
        req = urllib.request.Request(self._url, headers=headers, method="DELETE")
        res = send(req, self._timeout)
        if res.status not in (200, 204):
            raise error_from_body(res.status, res.body)

    def ping(self) -> None:
        self._rpc("ping")

    def list_tools(self) -> list[dict[str, Any]]:
        return self._rpc("tools/list").get("tools", [])

    def call(self, tool: str, arguments: dict[str, Any] | None = None, *, idempotency_key: str | None = None) -> dict[str, Any]:
        key = idempotency_key
        if key is None and tool in IDEMPOTENT_TOOLS:
            key = uuid.uuid4().hex
        params: dict[str, Any] = {"name": tool, "arguments": dict(arguments or {})}
        if key is not None:
            params["_meta"] = {"idempotencyKey": key}
        attempt = 0
        while True:
            try:
                return _tool_payload(self._rpc("tools/call", params))
            except SubspaceError as err:
                wait = self._retry_wait(err, keyed=key is not None, attempt=attempt)
                if wait is None:
                    raise
                attempt += 1
                self._sleep(wait)

    def _retry_wait(self, err: SubspaceError, *, keyed: bool, attempt: int) -> float | None:
        if attempt >= self._retries:
            return None
        if isinstance(err, TransportError):
            return backoff(attempt) if keyed else None
        if err.status == 429:
            return err.retry_after if err.retry_after is not None else backoff(attempt)
        return None

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {self._token}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": PROTOCOL_VERSION,
        }

    def _post(self, payload: dict[str, Any]) -> HTTPResult:
        headers = self._headers()
        if self._session is not None:
            headers["Mcp-Session-Id"] = self._session
        req = urllib.request.Request(
            self._url,
            data=json.dumps(payload).encode(),
            headers=headers,
            method="POST",
        )
        return send(req, self._timeout)

    def _notify(self, method: str) -> None:
        res = self._post({"jsonrpc": "2.0", "method": method})
        if res.status not in (200, 202):
            raise error_from_body(res.status, res.body, parse_retry_after(res.headers.get("Retry-After"), time.time()))

    def _rpc(self, method: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        if method != "initialize" and not self._initialized:
            self.initialize()
        try:
            return self._roundtrip(method, params)
        except SubspaceError as err:
            if err.status != 404 or self._session is None or method == "initialize":
                raise
            self._session = None
            self.initialize()
            return self._roundtrip(method, params)

    def _roundtrip(self, method: str, params: dict[str, Any] | None) -> dict[str, Any]:
        self._request_id += 1
        payload: dict[str, Any] = {"jsonrpc": "2.0", "id": self._request_id, "method": method}
        if params is not None:
            payload["params"] = params
        res = self._post(payload)
        if res.status != 200:
            raise error_from_body(res.status, res.body, parse_retry_after(res.headers.get("Retry-After"), time.time()))
        if method == "initialize":
            self._session = res.headers.get("Mcp-Session-Id") or self._session
        try:
            message = json.loads(res.body)
        except ValueError as err:
            raise SubspaceError("bad_response", f"invalid JSON-RPC response: {err}", status=res.status) from err
        if not isinstance(message, dict):
            raise SubspaceError("bad_response", "JSON-RPC response is not an object", status=res.status)
        if "error" in message:
            raise error_from_body(res.status, res.body)
        result = message.get("result")
        return result if isinstance(result, dict) else {}


def _tool_payload(result: dict[str, Any]) -> dict[str, Any]:
    structured = result.get("structuredContent")
    if result.get("isError"):
        err = structured.get("error") if isinstance(structured, dict) else None
        if not isinstance(err, dict):
            err = {}
        raise SubspaceError(
            str(err.get("code") or "tool_error"),
            str(err.get("message") or _text(result) or "tool call failed"),
            hint=err.get("hint"),
            details=err.get("details"),
        )
    if isinstance(structured, dict):
        return structured
    try:
        payload = json.loads(_text(result) or "{}")
    except ValueError:
        return {}
    return payload if isinstance(payload, dict) else {}


def _text(result: dict[str, Any]) -> str:
    content = result.get("content")
    if isinstance(content, list) and content and isinstance(content[0], dict):
        return str(content[0].get("text") or "")
    return ""
