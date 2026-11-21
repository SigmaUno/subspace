import contextlib
import unittest
from urllib.parse import parse_qs, urlparse

from helpers import Reply, ScriptedServer

from subspace_sdk import OwnerClient, SubspaceError
from subspace_sdk._transport import backoff

FIRST_CONNECTION = (
    "id: 1\nevent: hello\ndata: {\"type\":\"hello\",\"data\":{\"seq\":1}}\n\n"
    "id: 2\nevent: task.updated\ndata: {\"type\":\"task.updated\",\"ts\":\"2026-11-20T21:04:06Z\",\"data\":{\"id\":\"T-3\"}}\n\n"
)

RESUMED_CONNECTION = (
    ": ping\n\n"
    "event: hello\ndata: {\"type\":\"hello\",\"data\":{\"seq\":3}}\n\n"
    "id: 3\nevent: task.updated\ndata: {\"type\":\"task.updated\",\"data\":{\"id\":\"T-4\"}}\n\n"
    "id: 4\nevent: agent.updated\ndata: {\"type\":\"agent.updated\",\"data\":{\"id\":\"claude-1\"}}\n\n"
)


class EventStreamTests(unittest.TestCase):
    def setUp(self):
        self.sleeps = []

    def test_resumes_with_last_event_id_after_the_stream_drops(self):
        replies = iter([FIRST_CONNECTION, RESUMED_CONNECTION])

        def script(rec):
            return Reply(body=next(replies).encode(), headers={"Content-Type": "text/event-stream"})

        server = ScriptedServer(script)
        self.addCleanup(server.close)
        stream = OwnerClient(server.url, "tok", sleep=self.sleeps.append).events(["board", "agents"])
        with contextlib.closing(iter(stream)) as events:
            got = [next(events) for _ in range(4)]

        self.assertEqual([e.type for e in got], ["hello", "task.updated", "hello", "task.updated"])
        self.assertEqual([e.id for e in got], [1, 2, None, 3])
        self.assertEqual(got[1].data, {"id": "T-3"})
        self.assertEqual(stream.last_event_id, 3)
        self.assertEqual(len(server.requests), 2)
        self.assertNotIn("last-event-id", server.requests[0].headers)
        self.assertEqual(server.requests[1].headers["last-event-id"], "2")
        self.assertEqual(parse_qs(urlparse(server.requests[0].path).query), {"topics": ["board,agents"]})
        self.assertEqual(self.sleeps, [backoff(1)])

    def test_unauthorized_stream_raises_without_reconnecting(self):
        def script(rec):
            return Reply(status=401, body=b'{"error":{"code":"unauthorized","message":"bad token"}}')

        server = ScriptedServer(script)
        self.addCleanup(server.close)
        with self.assertRaises(SubspaceError) as cm:
            next(iter(OwnerClient(server.url, "tok", sleep=self.sleeps.append).events()))
        self.assertEqual(cm.exception.code, "unauthorized")
        self.assertEqual(cm.exception.status, 401)
        self.assertEqual(len(server.requests), 1)
        self.assertEqual(self.sleeps, [])
