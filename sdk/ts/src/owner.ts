import { errorFromResponse } from "./errors.ts";
import * as operations from "./generated/operations.ts";
import { follow } from "./stream.ts";
import type { FollowOptions } from "./stream.ts";
import { HttpTransport, joinUrl, withQuery } from "./transport.ts";
import type { FetchLike, Requester } from "./transport.ts";

type Bound<M> = {
  [K in keyof M as M[K] extends (r: Requester, ...args: never[]) => unknown ? K : never]: M[K] extends (
    r: Requester,
    ...args: infer A
  ) => infer R
    ? (...args: A) => R
    : never;
};

export type OwnerApi = Bound<typeof operations>;

export interface OwnerClientOptions {
  baseUrl: string;
  token: string;
  fetch?: FetchLike;
}

export interface EventsOptions extends FollowOptions {
  topics?: string[];
}

export interface ServerEvent<T = unknown> {
  id: string | undefined;
  type: string;
  ts: string | undefined;
  data: T;
}

export function bindOperations(requester: Requester): OwnerApi {
  const out: Record<string, unknown> = {};
  for (const [name, fn] of Object.entries(operations)) {
    out[name] = (...args: unknown[]) => (fn as (r: Requester, ...a: unknown[]) => unknown)(requester, ...args);
  }
  return out as OwnerApi;
}

export function decodeEvent(id: string | undefined, name: string, raw: string): ServerEvent {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { id, type: name, ts: undefined, data: raw };
  }
  if (typeof parsed === "object" && parsed !== null && "type" in parsed && "data" in parsed) {
    const env = parsed as { type: string; ts?: string; data: unknown };
    return { id, type: name === "message" ? env.type : name, ts: env.ts, data: env.data };
  }
  return { id, type: name, ts: undefined, data: parsed };
}

export class OwnerClient {
  readonly api: OwnerApi;
  readonly transport: HttpTransport;

  constructor(options: OwnerClientOptions) {
    this.transport = new HttpTransport(options);
    this.api = bindOperations(this.transport);
  }

  async *events(options: EventsOptions = {}): AsyncGenerator<ServerEvent> {
    const t = this.transport;
    const open = async (lastEventId: string | undefined, signal: AbortSignal | undefined) => {
      const query: Record<string, unknown> = {};
      if (options.topics !== undefined && options.topics.length > 0) query.topics = options.topics.join(",");
      if (lastEventId !== undefined) query.last_event_id = lastEventId;
      const res = await t.fetch(withQuery(joinUrl(t.baseUrl, "/api/v1/events"), query), {
        headers: t.headers({ accept: "text/event-stream" }),
        signal,
      });
      if (!res.ok) throw await errorFromResponse(res);
      return res;
    };
    for await (const msg of follow(open, options)) {
      yield decodeEvent(msg.id, msg.event, msg.data);
    }
  }
}
