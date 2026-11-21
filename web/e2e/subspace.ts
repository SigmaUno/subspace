import { execFile } from "node:child_process";
import { randomBytes } from "node:crypto";
import { promisify } from "node:util";
import { test as base, expect, type Page } from "@playwright/test";

export const apiURL = () => required("E2E_API_URL");
export const webURL = () => required("E2E_WEB_URL");
const run = promisify(execFile);

export const ownerToken = () => required("E2E_OWNER_TOKEN");

function required(name: string) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is not set; run through playwright.config.ts`);
  return value;
}

export function uniqueID(prefix: string) {
  return `${prefix}-${randomBytes(4).toString("hex")}`;
}

export async function owner<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  const response = await fetch(`${apiURL()}/api/v1/${path}`, {
    method,
    headers: { Authorization: `Bearer ${ownerToken()}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`${method} ${path}: HTTP ${response.status} ${await response.text()}`);
  return response.status === 204 ? undefined as T : response.json() as Promise<T>;
}

type ToolResult = { isError?: boolean; structuredContent?: Record<string, unknown> };

export class Agent {
  private session = "";
  private sequence = 0;

  constructor(readonly id: string, readonly token: string) {}

  private async post(message: Record<string, unknown>) {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
      Accept: "application/json, text/event-stream",
      "Content-Type": "application/json",
      "MCP-Protocol-Version": "2025-06-18",
    };
    if (this.session) headers["Mcp-Session-Id"] = this.session;
    const response = await fetch(`${apiURL()}/mcp`, { method: "POST", headers, body: JSON.stringify({ jsonrpc: "2.0", ...message }) });
    if (!response.ok) throw new Error(`MCP ${this.id}: HTTP ${response.status} ${await response.text()}`);
    this.session = response.headers.get("mcp-session-id") ?? this.session;
    return response.status === 202 ? null : await response.json() as { result?: unknown; error?: { message: string } };
  }

  private async request(method: string, params: Record<string, unknown>) {
    const reply = await this.post({ id: ++this.sequence, method, params });
    if (!reply || reply.error) throw new Error(`MCP ${this.id} ${method}: ${reply?.error?.message ?? "no response"}`);
    return reply.result;
  }

  async connect() {
    await this.request("initialize", {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "subspace-e2e", version: "1" },
    });
    await this.post({ method: "notifications/initialized" });
    return this;
  }

  async call<T = Record<string, unknown>>(name: string, args: Record<string, unknown> = {}): Promise<T> {
    const result = await this.request("tools/call", { name, arguments: args }) as ToolResult;
    if (result.isError) throw new Error(`${this.id} ${name}: ${JSON.stringify(result.structuredContent)}`);
    return result.structuredContent as T;
  }
}

export class HelperAgent {
  constructor(readonly id: string, private readonly token: string) {}

  async call<T = Record<string, unknown>>(tool: string, args: Record<string, unknown> = {}): Promise<T> {
    const env = { ...process.env, SUBSPACE_URL: apiURL(), SUBSPACE_TOKEN: this.token };
    try {
      const { stdout } = await run(required("E2E_HELPER_BIN"), [tool, JSON.stringify(args)], { env, timeout: 25_000 });
      return JSON.parse(stdout) as T;
    } catch (error) {
      const detail = error instanceof Error && "stderr" in error ? String(error.stderr) : String(error);
      throw new Error(`${this.id} ${tool}: ${detail.trim()}`);
    }
  }
}

export async function register(prefix: string, role: string, kind: string, provider: string) {
  const id = uniqueID(prefix);
  const { connect } = await owner<{ connect: { token: string } }>("agents", "POST", {
    id, role, kind, model: `${provider}-e2e`, provider,
  });
  return new Agent(id, connect.token).connect();
}

export const helperFor = (agent: Agent) => new HelperAgent(agent.id, agent.token);

export type Task = { id: string; title: string; status: string; assignee: string | null };
export type Plan = { id: string; brief_id: string; status: string; tasks: Task[] };
export type Brief = { id: string; title: string; plan_status: string };

export async function seedBrief() {
  const title = uniqueID("Brief");
  const brief = await owner<Brief>("briefs", "POST", {
    title,
    body: "Seeded by the browser end-to-end suite.",
    repos: ["SigmaUno/e2e"],
    max_cost_eur: 50,
    max_parallel_agents: 5,
    allowed_providers: [],
  });
  const leader = await register("lead", "leader", "other", "openai");
  const worker = await register("work", "worker", "claude-code", "anthropic");
  const { plan } = await leader.call<{ plan: Plan }>("propose_plan", {
    brief_id: brief.id,
    note: "Two tasks",
    tasks: [
      { ref: "api", title: `${title} API`, spec: "Add the API.", acceptance_criteria: ["tests pass"], repo: "SigmaUno/e2e" },
      { ref: "ui", title: `${title} UI`, spec: "Add the UI.", acceptance_criteria: ["renders"], repo: "SigmaUno/e2e", depends_on: ["api"] },
    ],
  });
  return { brief, plan, leader, worker };
}

export async function signIn(page: Page) {
  await page.context().addCookies([{ name: "subspace_owner", value: ownerToken(), url: webURL(), httpOnly: true, sameSite: "Strict" }]);
}

export const test = base.extend({
  baseURL: async ({}, provide) => { await provide(webURL()); },
});

export { expect };
