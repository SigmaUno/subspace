import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../src/index.ts";
import type { Types } from "../src/index.ts";
import { mcp, owner, planTask, register, unique } from "./env.ts";

test("registration, plan approval and task lifecycle", async () => {
  const client = owner();
  const lead = await register(client, "leader");
  const worker = await register(client, "worker");

  const agents = await client.api.getAgents({ query: { role: "worker" } });
  assert.ok(agents.agents?.some((a) => a.id === worker.id));
  const connect = await client.api.getAgentsByIdConnect({ path: { id: worker.id } });
  assert.equal(connect.agent_id, worker.id);
  assert.equal(connect.token, undefined);

  const brief = await client.api.postBriefs({
    body: { title: unique("brief"), body: "Ship it", repos: ["acme/app"], max_cost_eur: 50, max_parallel_agents: 2 },
  });
  assert.equal(brief.status, "open");

  const leader = mcp(lead.token);
  const worked = mcp(worker.token);
  try {
    const tools = await leader.listTools();
    assert.ok(tools.some((t) => t.name === "propose_plan"));
    assert.ok(!(await worked.listTools()).some((t) => t.name === "propose_plan"));

    const proposed = await leader.callTool<{ plan: Types.Plan }>("propose_plan", {
      brief_id: brief.id,
      tasks: [planTask("first")],
    });
    assert.equal(proposed.plan.status, "proposed");

    const plans = await client.api.getPlans({ query: { status: "proposed", brief_id: brief.id } });
    assert.deepEqual(
      plans.plans?.map((p) => p.id),
      [proposed.plan.id],
    );

    const approved = await client.api.postPlansByIdApprove({ path: { id: proposed.plan.id }, body: { note: "go" } });
    assert.equal(approved.status, "approved");

    const listed = await client.api.getTasks({ query: { brief_id: brief.id } });
    const task = listed.tasks?.[0];
    assert.ok(task);
    assert.equal(task.status, "ready");

    await leader.callTool("assign", { task_id: task.id, agent_id: worker.id });
    const mine = await worked.callTool<{ tasks: Types.Task[] }>("list_my_tasks", {});
    assert.deepEqual(
      mine.tasks.map((t) => t.id),
      [task.id],
    );

    const claimed = await worked.callTool<{ task: Types.Task }>("claim_task", { task_id: task.id });
    assert.equal(claimed.task.status, "in_progress");
    assert.equal((await client.api.getTasksById({ path: { id: task.id } })).status, "in_progress");

    const submitted = await worked.callTool<{ task: Types.Task }>("submit", {
      task_id: task.id,
      pr_url: "https://github.com/acme/app/pull/1",
      summary: "done",
    });
    assert.ok(["submitted", "approved"].includes(submitted.task.status));

    const board = await client.api.getBriefsByIdBoard({ path: { id: brief.id } });
    const column = board.columns?.[submitted.task.status];
    assert.equal(column?.length, 1);
  } finally {
    await leader.close();
    await worked.close();
  }
});

test("owner errors map to typed ApiError", async () => {
  const client = owner();

  await assert.rejects(client.api.getTasksById({ path: { id: "T-999999" } }), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 404);
    assert.equal(err.code, "not_found");
    return true;
  });

  await assert.rejects(client.api.postBriefs({ body: { title: "", body: "", repos: [], max_cost_eur: 1, max_parallel_agents: 1 } }), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 400);
    assert.equal(err.code, "bad_request");
    return true;
  });

  const worker = await register(client, "worker");
  const withAgentToken = owner((url, init) =>
    fetch(url, { ...init, headers: { ...(init?.headers as Record<string, string>), authorization: `Bearer ${worker.token}` } }),
  );
  await assert.rejects(withAgentToken.api.getState(), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 403);
    assert.equal(err.code, "forbidden");
    return true;
  });

  const anonymous = owner((url, init) => fetch(url, { ...init, headers: {} }));
  await assert.rejects(anonymous.api.getMe(), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 401);
    assert.equal(err.code, "unauthorized");
    return true;
  });

  const lead = await register(client, "leader");
  const brief = await client.api.postBriefs({
    body: { title: unique("brief"), body: "x", repos: ["acme/app"], max_cost_eur: 5, max_parallel_agents: 1 },
  });
  const leader = mcp(lead.token);
  try {
    const proposed = await leader.callTool<{ plan: Types.Plan }>("propose_plan", {
      brief_id: brief.id,
      tasks: [planTask("only")],
    });
    await client.api.postPlansByIdApprove({ path: { id: proposed.plan.id }, body: { note: "" } });
    await assert.rejects(client.api.postPlansByIdApprove({ path: { id: proposed.plan.id }, body: { note: "" } }), (err) => {
      assert.ok(err instanceof ApiError);
      assert.equal(err.status, 409);
      assert.equal(err.code, "conflict");
      return true;
    });
  } finally {
    await leader.close();
  }
});
