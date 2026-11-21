from __future__ import annotations

import email.utils
import http.client
import math
import urllib.error
import urllib.request
from dataclasses import dataclass
from email.message import Message
from typing import IO, Any

from .errors import TransportError


@dataclass(frozen=True)
class HTTPResult:
    status: int
    headers: Message
    body: bytes


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self, req: urllib.request.Request, fp: IO[bytes], code: int, msg: str, headers: Message, newurl: str
    ) -> None:
        return None


_opener = urllib.request.build_opener(_NoRedirect)


def urlopen(request: urllib.request.Request, timeout: float) -> Any:
    return _opener.open(request, timeout=timeout)


def send(request: urllib.request.Request, timeout: float) -> HTTPResult:
    try:
        with urlopen(request, timeout=timeout) as resp:
            return HTTPResult(resp.status, resp.headers, resp.read())
    except urllib.error.HTTPError as e:
        try:
            return HTTPResult(e.code, e.headers, e.read())
        finally:
            e.close()
    except (urllib.error.URLError, OSError, http.client.HTTPException) as e:
        raise TransportError(str(e)) from e


def parse_retry_after(value: str | None, now: float) -> float | None:
    if not value:
        return None
    try:
        seconds = float(value)
    except ValueError:
        try:
            when = email.utils.parsedate_to_datetime(value)
        except (TypeError, ValueError):
            return None
        seconds = when.timestamp() - now
    if not math.isfinite(seconds):
        return None
    return max(0.0, seconds)


def backoff(attempt: int) -> float:
    return min(0.1 * 2**attempt, 5.0)
