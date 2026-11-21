from __future__ import annotations

import json
import time
import urllib.parse
import urllib.request
from collections.abc import Callable, Iterator, Sequence
from typing import Any

from ._transport import backoff, parse_retry_after, send
from .errors import error_from_body
from .events import EventStream


class OwnerClient:
    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        timeout: float = 30.0,
        retries: int = 3,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self._base = base_url.rstrip("/")
        self._token = token
        self._timeout = timeout
        self._retries = retries
        self._sleep = sleep

    def me(self) -> dict[str, Any]:
        return self._request("GET", "/api/v1/me")

    def state(self) -> dict[str, Any]:
        return self._request("GET", "/api/v1/state")

    def pause_all(self, reason: str) -> dict[str, Any]:
        return self._request("POST", "/api/v1/pause-all", body={"reason": reason})

    def resume_all(self) -> dict[str, Any]:
        return self._request("POST", "/api/v1/resume-all", body={})

    def costs(
        self,
        *,
        group_by: str | None = None,
        split_by: str | None = None,
        since: str | None = None,
        until: str | None = None,
        brief_id: str | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "GET",
            "/api/v1/costs",
            query={"group_by": group_by, "split_by": split_by, "since": since, "until": until, "brief_id": brief_id},
        )

    def briefs(self, *, status: str | None = None) -> list[dict[str, Any]]:
        return list(self._paginate("/api/v1/briefs", "briefs", status=status))

    def brief(self, brief_id: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/briefs/{_seg(brief_id)}")

    def create_brief(
        self,
        title: str,
        body: str,
        *,
        repos: Sequence[str] | None = None,
        max_cost_eur: float | None = None,
        max_parallel_agents: int | None = None,
        allowed_providers: Sequence[str] | None = None,
        cross_review: bool | None = None,
        leader_id: str | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "title": title,
            "body": body,
            "repos": list(repos) if repos is not None else None,
            "max_cost_eur": max_cost_eur,
            "max_parallel_agents": max_parallel_agents,
            "allowed_providers": list(allowed_providers) if allowed_providers is not None else None,
            "cross_review": cross_review,
            "leader_id": leader_id,
        }
        return self._request("POST", "/api/v1/briefs", body=_drop_none(payload))

    def update_brief(self, brief_id: str, **fields: Any) -> dict[str, Any]:
        return self._request("PATCH", f"/api/v1/briefs/{_seg(brief_id)}", body=fields)

    def plans(self, *, status: str | None = None, brief_id: str | None = None) -> list[dict[str, Any]]:
        return list(self._paginate("/api/v1/plans", "plans", status=status, brief_id=brief_id))

    def plan(self, plan_id: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/plans/{_seg(plan_id)}")

    def approve_plan(self, plan_id: str, note: str = "") -> dict[str, Any]:
        return self._request("POST", f"/api/v1/plans/{_seg(plan_id)}/approve", body={"note": note})

    def reject_plan(self, plan_id: str, reason: str) -> dict[str, Any]:
        return self._request("POST", f"/api/v1/plans/{_seg(plan_id)}/reject", body={"reason": reason})

    def tasks(
        self,
        *,
        brief_id: str | None = None,
        status: str | None = None,
        assignee: str | None = None,
        role: str | None = None,
    ) -> list[dict[str, Any]]:
        return list(
            self._paginate(
                "/api/v1/tasks",
                "tasks",
                brief_id=brief_id,
                status=status,
                assignee=assignee,
                role=role,
            )
        )

    def task(self, task_id: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/tasks/{_seg(task_id)}")

    def mark_done(self, task_id: str, note: str = "") -> dict[str, Any]:
        return self._request("POST", f"/api/v1/tasks/{_seg(task_id)}/done", body={"note": note})

    def cancel_task(self, task_id: str, reason: str = "") -> dict[str, Any]:
        return self._request("POST", f"/api/v1/tasks/{_seg(task_id)}/cancel", body={"reason": reason})

    def agents(self, *, role: str | None = None, status: str | None = None) -> list[dict[str, Any]]:
        return list(self._paginate("/api/v1/agents", "agents", role=role, status=status))

    def agent(self, agent_id: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/agents/{_seg(agent_id)}")

    def register_agent(
        self,
        agent_id: str,
        *,
        role: str,
        kind: str,
        model: str,
        provider: str | None = None,
        expires_in: str | None = None,
    ) -> dict[str, Any]:
        payload = {
            "id": agent_id,
            "role": role,
            "kind": kind,
            "model": model,
            "provider": provider,
            "expires_in": expires_in,
        }
        return self._request("POST", "/api/v1/agents", body=_drop_none(payload))

    def register_agents_bulk(
        self,
        prefix: str,
        count: int,
        *,
        role: str,
        kind: str,
        model: str,
        provider: str | None = None,
        expires_in: str | None = None,
    ) -> list[dict[str, Any]]:
        payload = {
            "prefix": prefix,
            "count": count,
            "role": role,
            "kind": kind,
            "model": model,
            "provider": provider,
            "expires_in": expires_in,
        }
        res = self._request("POST", "/api/v1/agents/bulk", body=_drop_none(payload))
        return res["agents"]

    def decisions(self, *, limit: int | None = None) -> list[dict[str, Any]]:
        return self._request("GET", "/api/v1/decisions", query={"limit": limit})["decisions"]

    def next_decision(self) -> dict[str, Any] | None:
        return self._request("GET", "/api/v1/decisions/next")["decision"]

    def decide(self, decision_id: str, action: str, **params: Any) -> dict[str, Any]:
        return self._request("POST", f"/api/v1/decisions/{_seg(decision_id)}", body={"action": action, **params})

    def questions(self, *, status: str | None = "open") -> list[dict[str, Any]]:
        return list(self._paginate("/api/v1/questions", "questions", status=status))

    def answer_question(self, question_id: str, answer: str) -> dict[str, Any]:
        return self._request("POST", f"/api/v1/questions/{_seg(question_id)}/answer", body={"answer": answer})

    def events(
        self,
        topics: Sequence[str] = ("all",),
        *,
        last_event_id: int | None = None,
        timeout: float = 60.0,
        reconnect: bool = True,
    ) -> EventStream:
        return EventStream(
            self._base,
            self._token,
            topics,
            last_event_id=last_event_id,
            timeout=timeout,
            reconnect=reconnect,
            sleep=self._sleep,
        )

    def _paginate(self, path: str, key: str, **query: Any) -> Iterator[dict[str, Any]]:
        cursor: str | None = None
        while True:
            page = self._request("GET", path, query={**query, "cursor": cursor})
            yield from page[key]
            cursor = page.get("next_cursor")
            if not cursor:
                return

    def _request(
        self,
        method: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        body: dict[str, Any] | None = None,
    ) -> Any:
        url = self._base + path
        if query:
            pairs = [(k, _query_value(v)) for k, v in query.items() if v is not None]
            if pairs:
                url += "?" + urllib.parse.urlencode(pairs)
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Authorization": f"Bearer {self._token}", "Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        attempt = 0
        while True:
            req = urllib.request.Request(url, data=data, headers=headers, method=method)
            res = send(req, self._timeout)
            if 200 <= res.status < 300:
                return json.loads(res.body) if res.body else None
            retry_after = parse_retry_after(res.headers.get("Retry-After"), time.time())
            err = error_from_body(res.status, res.body, retry_after)
            if res.status != 429 or attempt >= self._retries:
                raise err
            self._sleep(retry_after if retry_after is not None else backoff(attempt))
            attempt += 1


def _seg(value: str) -> str:
    return urllib.parse.quote(value, safe="")


def _query_value(value: Any) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def _drop_none(payload: dict[str, Any]) -> dict[str, Any]:
    return {k: v for k, v in payload.items() if v is not None}
