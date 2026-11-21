import assert from "node:assert/strict";
import { test } from "node:test";
import type { ServerEvent } from "../src/index.ts";
import type { Types } from "../src/index.ts";
import { mcp, owner, planTask, register, unique, until } from "./env.ts";

async function proposal(count: number): Promise<{ plan: Types.Plan; ids: string[] }> {
  const client = owner();
  const lead = await register(client, "leader");
  const brief = await client.api.postBriefs({
    body: { title: unique("brief"), body: "events", repos: ["acme/app"], max_cost_eur: 50, max_parallel_agents: 4 },
  });
  const leader = mcp(lead.token);
  try {
    const out = await leader.callTool<{ plan: Types.Plan }>("propose_plan", {
      brief_id: brief.id,
      tasks: Array.from({ length: count }, (_, i) => planTask(`task ${i}`)),
    });
    const ids = (out.plan.tasks ?? []).flatMap((t) => (t === null ? [] : [t.id]));
    assert.equal(ids.length, count);
    return { plan: out.plan, ids };
  } finally {
    await leader.close();
  }
}

function taskIds(events: ServerEvent[]): Set<string> {
  const ids = new Set<string>();
  for (const e of events) {
    const task = e.data as { id?: string };
    if (e.type.startsWith("task.") && task.id !== undefined) ids.add(task.id);
  }
  return ids;
}

test("events resume after the last event id without gaps or duplicates", async () => {
  const client = owner();
  const first = await proposal(2);
  const second = await proposal(3);

  const live = new AbortController();
  const stream = client.events({ topics: ["board"], signal: live.signal, reconnect: false });
  const hello = await stream.next();
  assert.equal(hello.value?.type, "hello");
  const hearing = until(stream, (e) => e.type.startsWith("task.") && taskIds([e]).has(first.ids[1]!), live);
  await client.api.postPlansByIdApprove({ path: { id: first.plan.id }, body: { note: "" } });
  const seen = await hearing;
  const last = seen.filter((e) => e.id !== undefined).at(-1);
  assert.ok(last?.id);

  await client.api.postPlansByIdApprove({ path: { id: second.plan.id }, body: { note: "" } });

  const resumed = new AbortController();
  const replay = client.events({ topics: ["board"], lastEventId: last.id, signal: resumed.signal, reconnect: false });
  const events = await until(replay, (e) => taskIds([e]).has(second.ids[2]!), resumed);
  assert.equal(events[0]?.type, "hello");
  assert.equal(events[0]?.id, undefined);
  const ids = events.flatMap((e) => (e.id === undefined ? [] : [Number(e.id)]));
  assert.ok(ids.every((id) => id > Number(last.id)));
  assert.deepEqual(ids, [...new Set(ids)].sort((a, b) => a - b));
  const got = taskIds(events);
  for (const id of second.ids) assert.ok(got.has(id), `${id} was not replayed`);
  for (const id of first.ids) assert.ok(!got.has(id), `${id} was delivered twice`);
});

test("events reconnect on their own after the connection is cut mid-frame", async () => {
  const client = owner();
  const batch = await proposal(8);
  let opened = 0;
  const resumedFrom: (string | null)[] = [];
  const cutting = owner((url, init) => {
    const target = new URL(String(url));
    return fetch(url, init).then((res) => {
      if (!target.pathname.endsWith("/events") || res.body === null) return res;
      opened += 1;
      resumedFrom.push(target.searchParams.get("last_event_id"));
      if (opened > 3) return res;
      const upstream = res.body.getReader();
      let sent = 0;
      const body = new ReadableStream<Uint8Array>({
        async pull(controller) {
          const chunk = await upstream.read();
          if (chunk.done) return controller.close();
          const room = 900 - sent;
          if (chunk.value.length >= room) {
            controller.enqueue(chunk.value.slice(0, Math.max(0, room - 17)));
            await upstream.cancel();
            return controller.close();
          }
          sent += chunk.value.length;
          controller.enqueue(chunk.value);
        },
        cancel: () => upstream.cancel(),
      });
      return new Response(body, { status: res.status, headers: res.headers });
    });
  });

  const abort = new AbortController();
  const stream = cutting.events({
    topics: ["board"],
    signal: abort.signal,
    reconnect: { initialDelayMs: 10, maxDelayMs: 40 },
  });
  const wanted = new Set(batch.ids);
  const collected: ServerEvent[] = [];
  const collecting = until(
    stream,
    (e) => {
      collected.push(e);
      for (const id of taskIds([e])) wanted.delete(id);
      return wanted.size === 0;
    },
    abort,
  );
  await new Promise((r) => setTimeout(r, 200));
  await client.api.postPlansByIdApprove({ path: { id: batch.plan.id }, body: { note: "" } });
  await collecting;

  assert.ok(opened >= 2, `expected a reconnect, opened ${opened}`);
  assert.equal(resumedFrom[0], null);
  assert.ok(resumedFrom.slice(1).every((id) => id !== null && /^\d+$/.test(id)));
  const ids = collected.flatMap((e) => (e.id === undefined ? [] : [Number(e.id)]));
  assert.deepEqual(ids, [...new Set(ids)].sort((a, b) => a - b));
});

test("an invalid last event id is rejected, not retried", async () => {
  const client = owner();
  const abort = new AbortController();
  await assert.rejects(client.events({ lastEventId: "nope", signal: abort.signal }).next(), (err) => {
    assert.equal((err as { status?: number }).status, 400);
    return true;
  });
});
