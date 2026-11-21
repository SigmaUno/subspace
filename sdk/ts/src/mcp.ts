import { ApiError, McpRpcError, McpToolError, errorFromResponse } from "./errors.ts";
import { parseSse } from "./sse.ts";
import { follow } from "./stream.ts";
import type { FollowOptions } from "./stream.ts";
import type { FetchLike } from "./transport.ts";

export const protocolVersion = "2025-06-18";

export interface McpClientOptions {
  url: string;
  token: string;
  fetch?: FetchLike;
  clientInfo?: { name: string; version: string };
}

export interface McpTool {
  name: string;
  description?: string;
  inputSchema: Record<string, unknown>;
}

export interface InitializeResult {
  protocolVersion: string;
  capabilities: Record<string, unknown>;
  serverInfo: { name: string; version: string };
}

export interface ToolResult<T = unknown> {
  content: { type: string; text?: string }[];
  structuredContent?: T;
  isError: boolean;
}

export interface CallOptions {
  idempotencyKey?: string;
  signal?: AbortSignal;
}

export interface McpNotification {
  id: string | undefined;
  method: string;
  params: unknown;
}

interface RpcMessage {
  jsonrpc: "2.0";
  id?: number | string | null;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

export function toolError(tool: string, result: ToolResult): McpToolError {
  let payload: unknown = result.structuredContent;
  const text = result.content.find((c) => c.type === "text")?.text ?? "";
  if (payload === undefined) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = undefined;
    }
  }
  const err = typeof payload === "object" && payload !== null ? (payload as { error?: unknown }).error : undefined;
  if (typeof err === "object" && err !== null) {
    const e = err as { code?: unknown; message?: unknown; hint?: unknown; details?: unknown };
    return new McpToolError({
      tool,
      code: typeof e.code === "string" ? e.code : "internal",
      message: typeof e.message === "string" ? e.message : text,
      hint: typeof e.hint === "string" ? e.hint : undefined,
      details: e.details,
    });
  }
  return new McpToolError({ tool, code: "internal", message: text || "tool call failed" });
}

export class McpClient {
  readonly url: string;
  readonly #token: string;
  readonly #fetch: FetchLike;
  readonly #clientInfo: { name: string; version: string };
  #sessionId: string | undefined;
  #starting: Promise<InitializeResult> | undefined;
  #nextId = 1;
  serverInfo: InitializeResult["serverInfo"] | undefined;

  constructor(options: McpClientOptions) {
    this.url = options.url;
    this.#token = options.token;
    this.#fetch = options.fetch ?? fetch;
    this.#clientInfo = options.clientInfo ?? { name: "subspace-sdk", version: "0.1.0" };
  }

  get sessionId(): string | undefined {
    return this.#sessionId;
  }

  initialize(): Promise<InitializeResult> {
    this.#starting ??= this.#handshake().finally(() => {
      this.#starting = undefined;
    });
    return this.#starting;
  }

  async #handshake(): Promise<InitializeResult> {
    this.#sessionId = undefined;
    const result = await this.#send<InitializeResult>(
      "initialize",
      { protocolVersion, capabilities: {}, clientInfo: this.#clientInfo },
      undefined,
      false,
    );
    this.serverInfo = result.serverInfo;
    await this.notify("notifications/initialized");
    return result;
  }

  async #session(): Promise<void> {
    if (this.#sessionId === undefined) await this.initialize();
  }

  #headers(accept: string): Record<string, string> {
    const headers: Record<string, string> = {
      authorization: `Bearer ${this.#token}`,
      accept,
      "mcp-protocol-version": protocolVersion,
    };
    if (this.#sessionId !== undefined) headers["mcp-session-id"] = this.#sessionId;
    return headers;
  }

  async #post(message: RpcMessage, signal: AbortSignal | undefined, extra?: Record<string, string>): Promise<Response> {
    const res = await this.#fetch(this.url, {
      method: "POST",
      headers: { ...this.#headers("application/json, text/event-stream"), "content-type": "application/json", ...extra },
      body: JSON.stringify(message),
      signal,
    });
    if (!res.ok) throw await errorFromResponse(res);
    return res;
  }

  async #send<T>(
    method: string,
    params: unknown,
    signal: AbortSignal | undefined,
    retrySession: boolean,
    extra?: Record<string, string>,
  ): Promise<T> {
    const id = this.#nextId++;
    let res: Response;
    try {
      res = await this.#post({ jsonrpc: "2.0", id, method, params }, signal, extra);
    } catch (err) {
      if (retrySession && err instanceof ApiError && err.status === 404 && this.#sessionId !== undefined) {
        await this.initialize();
        return this.#send<T>(method, params, signal, false, extra);
      }
      throw err;
    }
    if (method === "initialize") this.#sessionId = res.headers.get("mcp-session-id") ?? undefined;
    const msg = await this.#reply(res, id);
    if (msg.error !== undefined) throw new McpRpcError(msg.error.code, msg.error.message, msg.error.data);
    return msg.result as T;
  }

  async #reply(res: Response, id: number): Promise<RpcMessage> {
    const type = res.headers.get("content-type") ?? "";
    if (type.startsWith("text/event-stream") && res.body !== null) {
      for await (const sse of parseSse(res.body)) {
        const msg = JSON.parse(sse.data) as RpcMessage;
        if (msg.id === id) return msg;
      }
      throw new McpRpcError(-32603, "stream ended without a response");
    }
    return (await res.json()) as RpcMessage;
  }

  async request<T = unknown>(method: string, params?: unknown, signal?: AbortSignal): Promise<T> {
    await this.#session();
    return this.#send<T>(method, params, signal, true);
  }

  async notify(method: string, params?: unknown): Promise<void> {
    const res = await this.#post({ jsonrpc: "2.0", method, params }, undefined);
    await res.body?.cancel();
  }

  async ping(): Promise<void> {
    await this.request("ping");
  }

  async listTools(): Promise<McpTool[]> {
    const tools: McpTool[] = [];
    let cursor: string | undefined;
    do {
      const page: { tools: McpTool[]; nextCursor?: string } = await this.request(
        "tools/list",
        cursor === undefined ? {} : { cursor },
      );
      tools.push(...page.tools);
      cursor = page.nextCursor;
    } while (cursor !== undefined);
    return tools;
  }

  async callToolRaw<T = unknown>(name: string, args: object = {}, options: CallOptions = {}): Promise<ToolResult<T>> {
    const params: { name: string; arguments: object; _meta?: { idempotencyKey: string } } = { name, arguments: args };
    if (options.idempotencyKey !== undefined) params._meta = { idempotencyKey: options.idempotencyKey };
    return this.request<ToolResult<T>>("tools/call", params, options.signal);
  }

  async callTool<T = unknown>(name: string, args: object = {}, options: CallOptions = {}): Promise<T> {
    const result = await this.callToolRaw<T>(name, args, options);
    if (result.isError) throw toolError(name, result);
    if (result.structuredContent !== undefined) return result.structuredContent;
    const text = result.content.find((c) => c.type === "text")?.text;
    return (text === undefined ? undefined : JSON.parse(text)) as T;
  }

  async close(): Promise<void> {
    const sid = this.#sessionId;
    if (sid === undefined) return;
    const headers = this.#headers("*/*");
    this.#sessionId = undefined;
    const res = await this.#fetch(this.url, { method: "DELETE", headers });
    await res.body?.cancel();
    if (!res.ok && res.status !== 404) throw await errorFromResponse(res);
  }

  async *notifications(options: FollowOptions = {}): AsyncGenerator<McpNotification> {
    await this.#session();
    const open = async (lastEventId: string | undefined, signal: AbortSignal | undefined): Promise<Response> => {
      const headers = this.#headers("text/event-stream");
      if (lastEventId !== undefined) headers["last-event-id"] = lastEventId;
      const res = await this.#fetch(this.url, { method: "GET", headers, signal });
      if (res.ok) return res;
      const err = await errorFromResponse(res);
      if (err.status === 404 && this.#sessionId !== undefined) {
        await this.initialize();
        return open(lastEventId, signal);
      }
      throw err;
    };
    for await (const sse of follow(open, options)) {
      const msg = JSON.parse(sse.data) as RpcMessage;
      if (msg.method === undefined || (msg.id !== undefined && msg.id !== null)) continue;
      yield { id: sse.id, method: msg.method, params: msg.params };
    }
  }
}
