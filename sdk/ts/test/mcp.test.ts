import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError, McpRpcError, McpToolError, protocolVersion } from "../src/index.ts";
import type { Types } from "../src/index.ts";
import { baseUrl, mcp, owner, rawOwnerToken, register, unique } from "./env.ts";

test("initialize creates a session that close ends", async () => {
  const worker = await register(owner(), "worker");
  const client = mcp(worker.token);
  const init = await client.initialize();
  assert.equal(init.protocolVersion, protocolVersion);
  assert.equal(init.serverInfo.name, "subspace");
  assert.ok(client.sessionId);
  await client.ping();
  const who = await client.callTool<{ agent: Types.Agent; paused: boolean }>("whoami");
  assert.equal(who.agent.id, worker.id);
  const sid = client.sessionId;
  await client.close();
  assert.equal(client.sessionId, undefined);
  const stale = await fetch(`${baseUrl}/mcp`, {
    method: "DELETE",
    headers: { authorization: `Bearer ${worker.token}`, "mcp-session-id": sid! },
  });
  assert.equal(stale.status, 404);
});

test("initializes on demand and recovers from a lost session", async () => {
  const worker = await register(owner(), "worker");
  let posts = 0;
  const client = mcp(worker.token, (url, init) => {
    const headers = new Headers(init?.headers);
    if (init?.method === "POST" && String(init.body).includes('"tools/list"')) {
      posts += 1;
      if (posts === 1) headers.set("mcp-session-id", "0".repeat(32));
    }
    return fetch(url, { ...init, headers });
  });
  assert.equal(client.sessionId, undefined);
  const [a, b] = await Promise.all([client.listTools(), client.listTools()]);
  assert.ok(a.length > 0);
  assert.deepEqual(
    a.map((t) => t.name),
    b.map((t) => t.name),
  );
  assert.ok(posts >= 3);
  await client.close();
});

test("tool errors carry code, hint and the tool name", async () => {
  const client = owner();
  const worker = await register(client, "worker");
  const c = mcp(worker.token);
  try {
    await assert.rejects(c.callTool("claim_task", { task_id: "T-424242" }), (err) => {
      assert.ok(err instanceof McpToolError);
      assert.equal(err.tool, "claim_task");
      assert.ok(["not_found", "not_assigned"].includes(err.code), err.code);
      assert.ok(err.message.length > 0);
      return true;
    });
    const raw = await c.callToolRaw("claim_task", { task_id: "T-424242" });
    assert.equal(raw.isError, true);
    const body = raw.structuredContent as { error: { code: string; hint?: string } };
    await assert.rejects(c.callTool("claim_task", { task_id: "T-424242" }), (err) => {
      assert.ok(err instanceof McpToolError);
      assert.equal(err.hint, body.error.hint);
      return true;
    });
    assert.ok(body.error.hint);
  } finally {
    await c.close();
  }
});

test("protocol and transport failures map to typed errors", async () => {
  const client = owner();
  const worker = await register(client, "worker");
  const c = mcp(worker.token);
  try {
    await assert.rejects(c.callTool("propose_plan", { brief_id: "B-1", tasks: [] }), (err) => {
      assert.ok(err instanceof McpRpcError);
      assert.equal(err.code, -32601);
      return true;
    });
    await assert.rejects(c.request("no/such/method"), (err) => {
      assert.ok(err instanceof McpRpcError);
      assert.equal(err.code, -32601);
      return true;
    });
  } finally {
    await c.close();
  }

  await assert.rejects(mcp("ssp_not-a-real-token").initialize(), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 401);
    assert.equal(err.code, "unauthorized");
    return true;
  });

  await assert.rejects(mcp(rawOwnerToken()).initialize(), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 403);
    assert.equal(err.code, "forbidden");
    return true;
  });
});

test("agent tokens are revoked on the server and the client sees 401", async () => {
  const client = owner();
  const worker = await register(client, "worker");
  const c = mcp(worker.token);
  await c.initialize();
  await client.api.postAgentsByIdRevoke({ path: { id: worker.id }, body: { reason: unique("revoked") } });
  await assert.rejects(c.listTools(), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 401);
    return true;
  });
});

test("the server has no notification stream and the client says so", async () => {
  const worker = await register(owner(), "worker");
  const c = mcp(worker.token);
  try {
    await assert.rejects(c.notifications().next(), (err) => {
      assert.ok(err instanceof ApiError);
      assert.equal(err.status, 405);
      return true;
    });
  } finally {
    await c.close();
  }
});
