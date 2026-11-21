import type { NextRequest } from "next/server";
import { apiError, browserMutation, ownerCookie, ownerStatus, upstream } from "../../../../lib/upstream";

type Context = RouteContext<"/api/v1/[...path]">;

const requestHeaders = ["range", "if-range", "if-none-match", "if-modified-since"];
const responseHeaders = [
  "content-type",
  "content-disposition",
  "content-length",
  "content-range",
  "accept-ranges",
  "etag",
  "last-modified",
  "content-security-policy",
  "x-content-type-options",
  "www-authenticate",
  "retry-after",
];

async function forward(request: NextRequest, context: Context) {
  const session = request.cookies.get(ownerCookie)?.value;
  const bearer = request.headers.get("authorization")?.match(/^Bearer ([^\s,]+)$/i)?.[1];
  const token = session || bearer;
  if (!token) return apiError(401, "Owner authentication required.");

  const readOnly = request.method === "GET";
  if (!readOnly && session && !browserMutation(request)) {
    return apiError(403, "Same-origin session required.");
  }

  const { path } = await context.params;
  if (path.some((part) => !/^[a-z0-9_:-]+$/i.test(part))) {
    return apiError(400, "Invalid API path.");
  }

  const events = path.length === 1 && path[0] === "events";
  const artifact = readOnly && path[0] === "artifacts" && path.length === 2;
  const headers = new Headers({
    Accept: events ? "text/event-stream" : artifact ? "*/*" : "application/json",
    "Accept-Encoding": "identity",
  });
  const contentType = request.headers.get("content-type");
  if (!readOnly && contentType) headers.set("Content-Type", contentType);
  const lastEventID = request.headers.get("last-event-id");
  if (events && lastEventID) headers.set("Last-Event-ID", lastEventID);
  if (readOnly) {
    for (const key of requestHeaders) {
      const value = request.headers.get(key);
      if (value) headers.set(key, value);
    }
  }

  try {
    if (!(readOnly && path.length === 1 && path[0] === "state")) {
      const status = await ownerStatus(token, request.signal);
      if (status === 401 || status === 403) return apiError(status, "Owner token rejected.");
      if (status !== 200) return apiError(502, "Subspace API is unavailable.");
    }
    const response = await upstream(`/api/v1/${path.join("/")}${request.nextUrl.search}`, token, {
      method: request.method,
      headers,
      body: readOnly ? undefined : request.body,
      duplex: "half",
      signal: request.signal,
    } as RequestInit & { duplex: "half" });
    const result = new Headers({ "Cache-Control": "no-store" });
    for (const key of responseHeaders) {
      const value = response.headers.get(key);
      if (value) result.set(key, value);
    }
    if (events) result.set("X-Accel-Buffering", "no");
    if (artifact) {
      result.set("Content-Security-Policy", "sandbox");
      result.set("X-Content-Type-Options", "nosniff");
      if (response.ok && !/^attachment\b/i.test(result.get("content-disposition") ?? "")) {
        result.set("Content-Disposition", "attachment");
      }
    }
    return new Response(response.body, { status: response.status, headers: result });
  } catch {
    return apiError(502, "Subspace API is unavailable.");
  }
}

export { forward as GET, forward as POST, forward as PUT, forward as PATCH, forward as DELETE };
