import unittest

from helpers import Reply, ScriptedServer

from subspace_sdk import MCPClient, OwnerClient, SubspaceError


class RedirectTests(unittest.TestCase):
    def setUp(self):
        self.elsewhere = ScriptedServer(lambda rec: Reply(body=b"{}"))
        self.addCleanup(self.elsewhere.close)
        target = self.elsewhere.url
        self.origin = ScriptedServer(lambda rec: Reply(status=302, headers={"Location": target + rec.path}))
        self.addCleanup(self.origin.close)

    def assert_token_stayed_home(self):
        for rec in self.elsewhere.requests:
            self.assertNotIn("authorization", rec.headers)

    def test_owner_client_does_not_follow_redirects_with_its_token(self):
        client = OwnerClient(self.origin.url, "ssu_secret", retries=0)
        with self.assertRaises(SubspaceError) as ctx:
            client.state()
        self.assertEqual(ctx.exception.status, 302)
        self.assert_token_stayed_home()

    def test_event_stream_does_not_follow_redirects_with_its_token(self):
        stream = OwnerClient(self.origin.url, "ssu_secret").events(reconnect=False)
        with self.assertRaises(SubspaceError):
            next(iter(stream))
        self.assert_token_stayed_home()

    def test_mcp_client_does_not_follow_redirects_with_its_token(self):
        client = MCPClient(self.origin.url + "/mcp", "ssp_secret", retries=0)
        with self.assertRaises(SubspaceError):
            client.ping()
        self.assert_token_stayed_home()


if __name__ == "__main__":
    unittest.main()
