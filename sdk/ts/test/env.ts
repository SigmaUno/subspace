import { McpClient, OwnerClient } from "../src/index.ts";
import type { FetchLike } from "../src/index.ts";

function required(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") throw new Error(`${name} is not set; run the suite with pnpm test`);
  return value;
}

export const baseUrl = required("SUBSPACE_TEST_URL");
const ownerToken = required("SUBSPACE_TEST_OWNER_TOKEN");

export function owner(fetchImpl?: FetchLike): OwnerClient {
  return new OwnerClient({ baseUrl, token: ownerToken, fetch: fetchImpl });
}

export function mcp(token: string, fetchImpl?: FetchLike): McpClient {
  return new McpClient({ url: `${baseUrl}/mcp`, token, fetch: fetchImpl });
}

export function rawOwnerToken(): string {
  return ownerToken;
}

let counter = 0;

export function unique(prefix: string): string {
  counter += 1;
  return `${prefix}-${Date.now().toString(36)}${counter}`;
}

export interface Agent {
  id: string;
  token: string;
}

const profiles: Record<string, { kind: string; provider: string }> = {
  leader: { kind: "other", provider: "other" },
  worker: { kind: "claude-code", provider: "anthropic" },
  tester: { kind: "codex", provider: "openai" },
  reviewer: { kind: "other", provider: "openai" },
};

export async function register(client: OwnerClient, role: keyof typeof profiles): Promise<Agent> {
  const id = unique(role);
  const profile = profiles[role]!;
  const out = await client.api.postAgents({
    body: { id, role, kind: profile.kind, model: "test-model", provider: profile.provider, expires_in: "" },
  });
  const token = out.connect.token;
  if (token === undefined) throw new Error("registration did not return a token");
  return { id, token };
}

export async function until<T>(
  gen: AsyncGenerator<T>,
  done: (item: T) => boolean,
  abort: AbortController,
  ms = 20_000,
): Promise<T[]> {
  const seen: T[] = [];
  const timer = setTimeout(() => abort.abort(), ms);
  try {
    for await (const item of gen) {
      seen.push(item);
      if (done(item)) return seen;
    }
  } finally {
    clearTimeout(timer);
    abort.abort();
  }
  throw new Error(`stream ended before the condition held; saw ${seen.length} items`);
}

export function planTask(title: string): Record<string, unknown> {
  return {
    title,
    spec: `Implement ${title}`,
    acceptance_criteria: ["it works"],
    repo: "acme/app",
  };
}
