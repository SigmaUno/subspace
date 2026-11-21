from __future__ import annotations

import http.client
import json
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable, Iterable, Iterator, Sequence
from dataclasses import dataclass
from typing import Any

from ._transport import backoff, parse_retry_after, urlopen
from .errors import SubspaceError, TransportError, error_from_body


@dataclass(frozen=True)
class Event:
    id: int | None
    type: str
    ts: str | None
    data: Any


class EventStream:
    def __init__(
        self,
        base_url: str,
        token: str,
        topics: Sequence[str] = ("all",),
        *,
        last_event_id: int | None = None,
        timeout: float = 60.0,
        reconnect: bool = True,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self._base = base_url.rstrip("/")
        self._token = token
        self._topics = ",".join(topics)
        self.last_event_id = last_event_id
        self._timeout = timeout
        self._reconnect = reconnect
        self._sleep = sleep
        self._closed = False
        self._response: Any = None

    def __iter__(self) -> Iterator[Event]:
        failures = 0
        while not self._closed:
            try:
                response = self._open()
            except SubspaceError as err:
                if not self._reconnect or not _retriable(err):
                    raise
                failures += 1
                self._sleep(backoff(failures))
                continue
            self._response = response
            try:
                for event in _parse(response):
                    if event.id is not None:
                        self.last_event_id = event.id
                    failures = 0
                    yield event
            except (OSError, http.client.HTTPException):
                pass
            finally:
                self._response = None
                response.close()
            if not self._reconnect or self._closed:
                return
            failures += 1
            self._sleep(backoff(failures))

    def close(self) -> None:
        self._closed = True
        if self._response is not None:
            self._response.close()

    def _open(self) -> Any:
        query = urllib.parse.urlencode({"topics": self._topics})
        headers = {"Authorization": f"Bearer {self._token}", "Accept": "text/event-stream"}
        if self.last_event_id is not None:
            headers["Last-Event-ID"] = str(self.last_event_id)
        req = urllib.request.Request(f"{self._base}/api/v1/events?{query}", headers=headers)
        try:
            return urlopen(req, timeout=self._timeout)
        except urllib.error.HTTPError as e:
            body = e.read()
            retry_after = parse_retry_after(e.headers.get("Retry-After"), time.time())
            e.close()
            raise error_from_body(e.code, body, retry_after) from None
        except (urllib.error.URLError, OSError, http.client.HTTPException) as e:
            raise TransportError(str(e)) from e


def _retriable(err: SubspaceError) -> bool:
    if isinstance(err, TransportError):
        return True
    return err.status is not None and (err.status == 429 or err.status >= 500)


def _parse(lines: Iterable[bytes]) -> Iterator[Event]:
    event_id: str | None = None
    name = ""
    data: list[str] = []
    for raw in lines:
        line = raw.decode("utf-8", "replace").rstrip("\r\n")
        if not line:
            if data:
                yield _event(event_id, name, "\n".join(data))
            event_id, name, data = None, "", []
            continue
        if line.startswith(":"):
            continue
        field, _, value = line.partition(":")
        if value.startswith(" "):
            value = value[1:]
        if field == "data":
            data.append(value)
        elif field == "event":
            name = value
        elif field == "id" and "\0" not in value:
            event_id = value


def _event(event_id: str | None, name: str, raw: str) -> Event:
    try:
        envelope = json.loads(raw)
    except ValueError:
        envelope = None
    if not isinstance(envelope, dict):
        envelope = {"data": raw}
    return Event(
        id=_as_int(event_id),
        type=str(envelope.get("type") or name or "message"),
        ts=envelope.get("ts"),
        data=envelope.get("data"),
    )


def _as_int(value: str | None) -> int | None:
    if value is not None and value.isascii() and value.isdigit():
        return int(value)
    return None
