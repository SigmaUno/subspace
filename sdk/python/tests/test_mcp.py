import json
import unittest

from helpers import Reply, ScriptedServer, rpc_result, tool_result

from subspace_sdk import MCPClient, SubspaceError, TransportError
from subspace_sdk._transport import backoff


def mcp_script(on_call):
    def script(rec):
        if rec.method == "DELETE":
            return Reply(status=204)
        msg = rec.json()
        method = msg.get("method")
        if method == "initialize":
            return Reply(body=rpc_result(msg, {"protocolVersion": "2025-06-18"}), headers={"Mcp-Session-Id": "sess-1"})
        if "id" not in msg:
            return Reply(status=202)
        if method == "tools/call":
            return on_call(rec, msg)
        return Reply(body=rpc_result(msg, {}))

    return script


class MCPClientTests(unittest.TestCase):
    def setUp(self):
        self.sleeps = []

    def connect(self, on_call, retries=3):
        server = ScriptedServer(mcp_script(on_call))
        self.addCleanup(server.close)
        client = MCPClient(server.url + "/mcp", "tok", retries=retries, sleep=self.sleeps.append)
        self.addCleanup(client.close)
        return server, client

    def test_rate_limited_call_waits_for_retry_after_and_reuses_key(self):
        attempts = []

        def on_call(rec, msg):
            attempts.append(msg)
            if len(attempts) == 1:
                return Reply(
                    status=429,
                    headers={"Retry-After": "2"},
                    body=json.dumps({"error": {"code": "rate_limited", "message": "slow down"}}).encode(),
                )
            return Reply(body=tool_result(msg, {"ok": True}))

        _, mcp = self.connect(on_call)
        self.assertEqual(mcp.call("claim_task", {"task_id": "T-1"}), {"ok": True})
        self.assertEqual(self.sleeps, [2.0])
        keys = [m["params"]["_meta"]["idempotencyKey"] for m in attempts]
        self.assertEqual(len(keys), 2)
        self.assertTrue(keys[0])
        self.assertEqual(keys[0], keys[1])

    def test_dropped_connection_on_idempotent_tool_is_retried_with_same_key(self):
        attempts = []

        def on_call(rec, msg):
            attempts.append(msg)
            if len(attempts) == 1:
                return Reply(drop=True)
            return Reply(body=tool_result(msg, {"ok": True}))

        _, mcp = self.connect(on_call)
        self.assertEqual(mcp.call("submit", {"task_id": "T-1", "pr_url": "u", "summary": "s"}), {"ok": True})
        self.assertEqual(self.sleeps, [backoff(0)])
        self.assertEqual(attempts[0]["params"]["_meta"], attempts[1]["params"]["_meta"])

    def test_dropped_connection_on_plain_tool_is_not_retried(self):
        attempts = []

        def on_call(rec, msg):
            attempts.append(msg)
            return Reply(drop=True)

        _, mcp = self.connect(on_call)
        with self.assertRaises(TransportError):
            mcp.call("whoami")
        self.assertEqual(len(attempts), 1)
        self.assertNotIn("_meta", attempts[0]["params"])
        self.assertEqual(self.sleeps, [])

    def test_tool_error_surfaces_code_and_hint_without_retry(self):
        attempts = []

        def on_call(rec, msg):
            attempts.append(msg)
            structured = {"error": {"code": "not_assigned", "message": "task T-1 is not assigned to you", "hint": "ask the leader"}}
            return Reply(body=rpc_result(msg, {"content": [], "structuredContent": structured, "isError": True}))

        _, mcp = self.connect(on_call)
        with self.assertRaises(SubspaceError) as cm:
            mcp.call("claim_task", {"task_id": "T-1"})
        self.assertEqual(cm.exception.code, "not_assigned")
        self.assertEqual(cm.exception.hint, "ask the leader")
        self.assertEqual(len(attempts), 1)
        self.assertEqual(self.sleeps, [])

    def test_session_is_reinitialized_after_404(self):
        attempts = []

        def on_call(rec, msg):
            attempts.append(rec)
            if len(attempts) == 1:
                return Reply(status=404, body=b"unknown session")
            return Reply(body=tool_result(msg, {"ok": True}))

        server, mcp = self.connect(on_call)
        self.assertEqual(mcp.call("whoami"), {"ok": True})
        initializations = [r for r in server.requests if r.json().get("method") == "initialize"]
        self.assertEqual(len(initializations), 2)
        self.assertEqual(attempts[-1].headers["mcp-session-id"], "sess-1")
