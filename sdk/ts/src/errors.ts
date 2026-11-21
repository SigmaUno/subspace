export type ErrorCode =
  | "bad_request"
  | "unauthorized"
  | "token_expired"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "gates_failed"
  | "paused"
  | "capacity"
  | "rate_limited"
  | "too_large"
  | "quota_exceeded"
  | "internal"
  | (string & {});

export class SubspaceError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = new.target.name;
  }
}

export class ApiError extends SubspaceError {
  readonly status: number;
  readonly code: ErrorCode;
  readonly details: unknown;
  readonly body: unknown;
  readonly retryAfterMs: number | undefined;

  constructor(init: {
    status: number;
    code: ErrorCode;
    message: string;
    details?: unknown;
    body?: unknown;
    retryAfterMs?: number;
  }) {
    super(init.message);
    this.status = init.status;
    this.code = init.code;
    this.details = init.details;
    this.body = init.body;
    this.retryAfterMs = init.retryAfterMs;
  }
}

export class McpRpcError extends SubspaceError {
  readonly code: number;
  readonly data: unknown;

  constructor(code: number, message: string, data?: unknown) {
    super(message);
    this.code = code;
    this.data = data;
  }
}

export class McpToolError extends SubspaceError {
  readonly tool: string;
  readonly code: ErrorCode;
  readonly hint: string | undefined;
  readonly details: unknown;

  constructor(init: { tool: string; code: ErrorCode; message: string; hint?: string; details?: unknown }) {
    super(init.message);
    this.tool = init.tool;
    this.code = init.code;
    this.hint = init.hint;
    this.details = init.details;
  }
}

const statusCodes: Record<number, ErrorCode> = {
  400: "bad_request",
  401: "unauthorized",
  403: "forbidden",
  404: "not_found",
  409: "conflict",
  413: "too_large",
  423: "paused",
  429: "rate_limited",
};

function retryAfter(res: Response): number | undefined {
  const raw = res.headers.get("retry-after");
  if (raw === null) return undefined;
  const seconds = Number(raw);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000;
  const at = Date.parse(raw);
  return Number.isNaN(at) ? undefined : Math.max(0, at - Date.now());
}

export async function errorFromResponse(res: Response): Promise<ApiError> {
  const text = await res.text().catch(() => "");
  let body: unknown = text;
  try {
    body = JSON.parse(text);
  } catch {
    body = text;
  }
  const fallback = statusCodes[res.status] ?? (res.status >= 500 ? "internal" : "bad_request");
  const err = typeof body === "object" && body !== null ? (body as { error?: unknown }).error : undefined;
  if (typeof err === "object" && err !== null) {
    const e = err as { code?: unknown; message?: unknown; details?: unknown };
    return new ApiError({
      status: res.status,
      code: typeof e.code === "string" ? e.code : fallback,
      message: typeof e.message === "string" ? e.message : res.statusText,
      details: e.details,
      body,
      retryAfterMs: retryAfter(res),
    });
  }
  return new ApiError({
    status: res.status,
    code: fallback,
    message: text.trim() || res.statusText,
    body,
    retryAfterMs: retryAfter(res),
  });
}
