import contextlib
import secrets
import unittest

import helpers
import live

from subspace_sdk import MCPClient, OwnerClient, SubspaceError

server = None


def setUpModule():
    global server
    server = live.start()


def tearDownModule():
    if server is not None:
        server.stop()


def unique(prefix):
    return f"{prefix}-{secrets.token_hex(3)}"


class LiveServerTests(unittest.TestCase):
    def setUp(self):
        self.owner = OwnerClient(server.url, server.owner_token)

    def test_owner_identity_briefs_and_costs(self):
        self.assertEqual(self.owner.me()["scope"], "owner")
        title = unique("sdk brief")
        created = self.owner.create_brief(title, "markdown body", repos=["o/r"], max_cost_eur=5, max_parallel_agents=2)
        self.assertEqual(self.owner.brief(created["id"])["title"], title)
        self.assertIn(created["id"], [b["id"] for b in self.owner.briefs()])
        self.assertIsInstance(self.owner.costs(group_by="brief"), dict)

    def test_api_errors_are_typed(self):
        with self.assertRaises(SubspaceError) as cm:
            self.owner.task("T-999999")
        self.assertEqual(cm.exception.code, "not_found")
        self.assertEqual(cm.exception.status, 404)

    def test_pause_and_resume(self):
        self.assertTrue(self.owner.pause_all("sdk test")["paused"])
        self.assertTrue(self.owner.state()["paused"])
        self.assertFalse(self.owner.resume_all()["paused"])
        self.assertFalse(self.owner.state()["paused"])

    def test_bulk_registration_returns_connect_info(self):
        prefix = unique("bulk")
        agents = self.owner.register_agents_bulk(prefix, 2, role="worker", kind="claude-code", model="claude-sonnet-5-5")
        self.assertEqual([a["agent"]["id"] for a in agents], [f"{prefix}-001", f"{prefix}-002"])
        self.assertTrue(agents[0]["connect"]["token"].startswith("ssp_"))

    def test_worker_sees_only_its_tools_and_identity(self):
        agent_id = unique("worker")
        connect = self.owner.register_agent(agent_id, role="worker", kind="claude-code", model="claude-sonnet-5-5")["connect"]
        with MCPClient(server.url + "/mcp", connect["token"]) as mcp:
            mcp.initialize()
            self.assertEqual(mcp.call("whoami")["agent"]["id"], agent_id)
            names = {tool["name"] for tool in mcp.list_tools()}
            self.assertIn("whoami", names)
            self.assertNotIn("create_task", names)
            with self.assertRaises(SubspaceError) as cm:
                mcp.call("create_task", {"brief_id": "B-1", "title": "x", "spec": "y", "repo": "o/r"})
            self.assertEqual(cm.exception.details, {"rpc_code": -32601})

    def test_owner_token_is_rejected_on_mcp(self):
        with MCPClient(server.url + "/mcp", server.owner_token) as mcp:
            with self.assertRaises(SubspaceError) as cm:
                mcp.initialize()
        self.assertEqual(cm.exception.status, 403)

    def test_leader_plan_is_approved_and_rejected_through_owner_api(self):
        leader_id = unique("lead")
        connect = self.owner.register_agent(leader_id, role="leader", kind="other", model="m", provider="anthropic")["connect"]
        repo = "o/r"
        task = {"title": "t1", "spec": "do it", "acceptance_criteria": ["works"], "repo": repo}
        with MCPClient(server.url + "/mcp", connect["token"]) as leader:
            leader.initialize()
            first = self.owner.create_brief(unique("plan approve"), "body", repos=[repo])
            plan_id = leader.call("propose_plan", {"brief_id": first["id"], "tasks": [task]})["plan"]["id"]
            self.assertEqual([p["id"] for p in self.owner.plans(status="proposed", brief_id=first["id"])], [plan_id])
            self.assertEqual(self.owner.approve_plan(plan_id)["status"], "approved")
            self.assertEqual([t["status"] for t in self.owner.tasks(brief_id=first["id"])], ["ready"])

            second = self.owner.create_brief(unique("plan reject"), "body", repos=[repo])
            plan_id = leader.call("propose_plan", {"brief_id": second["id"], "tasks": [task]})["plan"]["id"]
            self.assertEqual(self.owner.reject_plan(plan_id, "not now")["status"], "rejected")
            self.assertEqual([t["status"] for t in self.owner.tasks(brief_id=second["id"])], ["cancelled"])

    def test_event_stream_starts_with_hello(self):
        with contextlib.closing(iter(self.owner.events(["all"]))) as events:
            first = next(events)
        self.assertEqual(first.type, "hello")
        self.assertIsInstance(first.data["seq"], int)
