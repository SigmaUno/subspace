from __future__ import annotations

import http.server
import json
import sys
import threading
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


@dataclass(frozen=True)
class Recorded:
    method: str
    path: str
    headers: dict[str, str]
    body: bytes

    def json(self) -> dict:
        return json.loads(self.body or b"{}")


@dataclass(frozen=True)
class Reply:
    status: int = 200
    body: bytes = b""
    headers: dict[str, str] = field(default_factory=dict)
    drop: bool = False


class ScriptedServer:
    def __init__(self, script: Callable[[Recorded], Reply]) -> None:
        self.script = script
        self.requests: list[Recorded] = []
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: object) -> None:
                return

            def _dispatch(self) -> None:
                owner._serve(self)

            do_GET = _dispatch
            do_POST = _dispatch
            do_DELETE = _dispatch

        self._httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._httpd.serve_forever, daemon=True)
        self._thread.start()

    def _serve(self, handler: http.server.BaseHTTPRequestHandler) -> None:
        length = int(handler.headers.get("Content-Length") or 0)
        body = handler.rfile.read(length) if length else b""
        rec = Recorded(handler.command, handler.path, {k.lower(): v for k, v in handler.headers.items()}, body)
        self.requests.append(rec)
        reply = self.script(rec)
        if reply.drop:
            handler.close_connection = True
            return
        handler.send_response(reply.status)
        for name, value in reply.headers.items():
            handler.send_header(name, value)
        handler.send_header("Content-Length", str(len(reply.body)))
        handler.end_headers()
        handler.wfile.write(reply.body)

    @property
    def url(self) -> str:
        host, port = self._httpd.server_address[:2]
        return f"http://{host}:{port}"

    def close(self) -> None:
        self._httpd.shutdown()
        self._httpd.server_close()
        self._thread.join(timeout=5)


def rpc_result(msg: dict, result: dict) -> bytes:
    return json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}).encode()


def rpc_error(msg: dict, code: int, message: str) -> bytes:
    return json.dumps({"jsonrpc": "2.0", "id": msg.get("id"), "error": {"code": code, "message": message}}).encode()


def tool_result(msg: dict, structured: dict) -> bytes:
    return rpc_result(
        msg,
        {"content": [{"type": "text", "text": json.dumps(structured)}], "structuredContent": structured, "isError": False},
    )
