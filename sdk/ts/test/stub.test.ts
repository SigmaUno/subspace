import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { IncomingMessage, Server, ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import { McpClient, McpToolError, parseSse } from "../src/index.ts";
import type { McpNotification, SseMessage } from "../src/index.ts";

function chunked(text: string, size: number): ReadableStream<Uint8Array> {
  const bytes = new TextEncoder().encode(text);
  let at = 0;
  return new ReadableStream({
    pull(controller) {
      if (at >= bytes.length) return controller.close();
      controller.enqueue(bytes.slice(at, at + size));
      at += size;
    },
  });
}

async function collect(stream: ReadableStream<Uint8Array>): Promise<SseMessage[]> {
  const out: SseMessage[] = [];
  for await (const msg of parseSse(stream)) out.push(msg);
  return out;
}

test("the SSE parser is independent of chunk boundaries", async () => {
  const text =
    ": ping\r\n\r\n" +
    "id: 7\r\nevent: task.updated\r\ndata: {\"a\":\r\ndata: \"é€\"}\r\n\r\n" +
    "retry: 1500\ndata: second\n\n" +
    "data: lone-cr\r\r" +
    "id: 9\nevent: resync\ndata: {}\n\n" +
    "data: unterminated";
  const want = await collect(chunked(text, text.length));
  assert.deepEqual(
    want.map((m) => [m.id, m.event, m.data, m.retry]),
    [
      ["7", "task.updated", '{"a":\n"é€"}', undefined],
      ["7", "message", "second", 1500],
      ["7", "message", "lone-cr", undefined],
      ["9", "resync", "{}", undefined],
    ],
  );
  for (const size of [1, 2, 3, 5, 8, 13]) {
    assert.deepEqual(await collect(chunked(text, size)), want, `chunk size ${size}`);
  }
});

interface Stub {
  url: string;
  close(): Promise<void>;
  log: { method: string; session: string | undefined; lastEventId: string | undefined; rpc?: string }[];
}

async function stub(handler: (req: IncomingMessage, res: ServerResponse, body: string, n: number) => void): Promise<Stub> {
  const log: Stub["log"] = [];
  let n = 0;
  const server: Server = createServer((req, res) => {
    let body = "";
    req.setEncoding("utf8");
    req.on("data", (c: string) => (body += c));
    req.on("end", () => {
      n += 1;
      let rpc: string | undefined;
      try {
        rpc = (JSON.parse(body) as { method?: string }).method;
      } catch {
        rpc = undefined;
      }
      log.push({
        method: req.method ?? "",
        session: req.headers["mcp-session-id"] as string | undefined,
        lastEventId: req.headers["last-event-id"] as string | undefined,
        rpc,
      });
      handler(req, res, body, n);
    });
  });
  await new Promise<void>((ok) => server.listen(0, "127.0.0.1", ok));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}/mcp`,
    log,
    close: () =>
      new Promise<void>((ok) => {
        server.closeAllConnections();
        server.close(() => ok());
      }),
  };
}

function reply(res: ServerResponse, id: unknown, result: unknown, headers: Record<string, string> = {}): void {
  res.writeHead(200, { "content-type": "application/json", ...headers });
  res.end(JSON.stringify({ jsonrpc: "2.0", id, result }));
}

function rpcOf(body: string): { id?: number; method?: string } {
  return JSON.parse(body) as { id?: number; method?: string };
}

test("tool errors expose hint and details, and POST replies may arrive as an event stream", async () => {
  const details = { task_id: "T-3", gates: [{ gate: "tests", passed: false }] };
  const s = await stub((_req, res, body) => {
    const rpc = rpcOf(body);
    if (rpc.id === undefined) {
      res.writeHead(202).end();
      return;
    }
    if (rpc.method === "initialize") {
      return reply(res, rpc.id, { protocolVersion: "2025-06-18", capabilities: {}, serverInfo: { name: "stub", version: "1" } }, { "mcp-session-id": "s1" });
    }
    const payload = { error: { code: "gates_failed", message: "gates hold the approval", hint: "fix the tests", details } };
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write(`data: ${JSON.stringify({ jsonrpc: "2.0", method: "notifications/progress", params: {} })}\n\n`);
    res.end(
      `id: 1\ndata: ${JSON.stringify({
        jsonrpc: "2.0",
        id: rpc.id,
        result: { content: [{ type: "text", text: JSON.stringify(payload) }], structuredContent: payload, isError: true },
      })}\n\n`,
    );
  });
  try {
    const client = new McpClient({ url: s.url, token: "t" });
    await assert.rejects(client.callTool("review", { task_id: "T-3" }), (err) => {
      assert.ok(err instanceof McpToolError);
      assert.equal(err.code, "gates_failed");
      assert.equal(err.hint, "fix the tests");
      assert.deepEqual(err.details, details);
      assert.equal(err.tool, "review");
      return true;
    });
  } finally {
    await s.close();
  }
});

test("notifications reconnect with the session and last event id and re-initialize on 404", async () => {
  let sessions = 0;
  let streams = 0;
  const s = await stub((req, res, body) => {
    if (req.method === "POST") {
      const rpc = rpcOf(body);
      if (rpc.id === undefined) {
        res.writeHead(202).end();
        return;
      }
      sessions += 1;
      return reply(res, rpc.id, { protocolVersion: "2025-06-18", capabilities: {}, serverInfo: { name: "stub", version: "1" } }, { "mcp-session-id": `s${sessions}` });
    }
    streams += 1;
    const note = (id: number, method: string) =>
      `id: ${id}\ndata: ${JSON.stringify({ jsonrpc: "2.0", method, params: { n: id } })}\n\n`;
    if (streams === 1) {
      res.writeHead(404).end("unknown session");
      return;
    }
    res.writeHead(200, { "content-type": "text/event-stream" });
    if (streams === 2) {
      res.write(": hello\n\n" + note(1, "notifications/tools/list_changed") + note(2, "notifications/message"));
      res.write(`data: ${JSON.stringify({ jsonrpc: "2.0", id: 99, method: "ping" })}\n\n`);
      res.end();
      return;
    }
    res.write(`retry: 5\n` + note(3, "notifications/message"));
    res.end();
  });
  try {
    const client = new McpClient({ url: s.url, token: "t" });
    const abort = new AbortController();
    const got: McpNotification[] = [];
    for await (const n of client.notifications({ signal: abort.signal, reconnect: { initialDelayMs: 5, maxDelayMs: 20 } })) {
      got.push(n);
      if (got.length === 3) abort.abort();
    }
    assert.deepEqual(
      got.map((n) => [n.id, n.method, (n.params as { n: number }).n]),
      [
        ["1", "notifications/tools/list_changed", 1],
        ["2", "notifications/message", 2],
        ["3", "notifications/message", 3],
      ],
    );
    const gets = s.log.filter((l) => l.method === "GET");
    assert.equal(sessions, 2);
    assert.deepEqual(
      gets.map((g) => [g.session, g.lastEventId]),
      [
        ["s1", undefined],
        ["s2", undefined],
        ["s2", "2"],
      ],
    );
  } finally {
    await s.close();
  }
});
