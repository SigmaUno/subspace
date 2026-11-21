import { errorFromResponse } from "./errors.ts";

export type FetchLike = typeof fetch;

export type ResponseKind = "json" | "text" | "bytes" | "none";

export interface RequestInput {
  path?: Record<string, string | number>;
  query?: Record<string, unknown>;
  body?: unknown;
  contentType?: string;
  response: ResponseKind;
  signal?: AbortSignal;
}

export interface Requester {
  request<T>(method: string, route: string, input: RequestInput): Promise<T>;
}

export interface HttpTransportOptions {
  baseUrl: string;
  token: string;
  fetch?: FetchLike;
}

export function expand(route: string, path: Record<string, string | number> | undefined): string {
  return route.replace(/\{([^}]+)\}/g, (_, name: string) => {
    const value = path?.[name];
    if (value === undefined) throw new TypeError(`missing path parameter ${name} for ${route}`);
    return encodeURIComponent(String(value));
  });
}

export function withQuery(url: string, query: Record<string, unknown> | undefined): string {
  if (query === undefined) return url;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null) continue;
    params.set(key, String(value));
  }
  const text = params.toString();
  return text === "" ? url : `${url}?${text}`;
}

export function joinUrl(base: string, path: string): string {
  return base.replace(/\/+$/, "") + path;
}

export class HttpTransport implements Requester {
  readonly baseUrl: string;
  readonly #token: string;
  readonly #fetch: FetchLike;

  constructor(options: HttpTransportOptions) {
    this.baseUrl = options.baseUrl;
    this.#token = options.token;
    this.#fetch = options.fetch ?? fetch;
  }

  headers(extra?: Record<string, string>): Record<string, string> {
    return { authorization: `Bearer ${this.#token}`, ...extra };
  }

  get fetch(): FetchLike {
    return this.#fetch;
  }

  async request<T>(method: string, route: string, input: RequestInput): Promise<T> {
    const url = withQuery(joinUrl(this.baseUrl, expand(route, input.path)), input.query);
    const headers = this.headers({ accept: input.response === "json" ? "application/json" : "*/*" });
    let body: RequestInit["body"];
    if (input.body !== undefined) {
      if (input.contentType === undefined) {
        headers["content-type"] = "application/json";
        body = JSON.stringify(input.body);
      } else {
        if (!(input.body instanceof FormData) && !(input.body instanceof URLSearchParams)) {
          headers["content-type"] = input.contentType;
        }
        body = input.body as RequestInit["body"];
      }
    }
    const res = await this.#fetch(url, { method, headers, body, signal: input.signal });
    if (!res.ok) throw await errorFromResponse(res);
    switch (input.response) {
      case "none":
        await res.body?.cancel();
        return undefined as T;
      case "text":
        return (await res.text()) as T;
      case "bytes":
        return new Uint8Array(await res.arrayBuffer()) as T;
      case "json":
        return (await res.json()) as T;
    }
  }
}
