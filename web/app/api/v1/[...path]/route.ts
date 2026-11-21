import type { NextRequest } from "next/server";
import { apiError, browserMutation, ownerCookie, ownerStatus, upstream } from "../../../../lib/upstream";

type Context = RouteContext<"/api/v1/[...path]">;

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
  const headers = new Headers({
    Accept: events ? "text/event-stream" : "application/json",
    "Accept-Encoding": "identity",
  });
  const contentType = request.headers.get("content-type");
  if (!readOnly && contentType) headers.set("Content-Type", contentType);
  const lastEventID = request.headers.get("last-event-id");
  if (events && lastEventID) headers.set("Last-Event-ID", lastEventID);

  try {
    if (!(readOnly && path.length === 1 && path[0] === "state")) {
      const status = await ownerStatus(token, request.signal);
      if (status === 401 || status === 403) return apiError(status, "Owner token rejected.");
      if (status !== 200) return apiError(502, "Subspace API is unavailable.");
    }
    const response = await upstream(`/api/v1/${path.join("/")}${request.nextUrl.search}`, token, {
      method: request.method,
      headers,
      body: readOnly ? undefined : await request.text(),
      signal: request.signal,
    });
    const responseHeaders = new Headers({ "Cache-Control": "no-store" });
    for (const key of ["content-type", "www-authenticate", "retry-after"]) {
      const value = response.headers.get(key);
      if (value) responseHeaders.set(key, value);
    }
    if (events) responseHeaders.set("X-Accel-Buffering", "no");
    return new Response(response.body, { status: response.status, headers: responseHeaders });
  } catch {
    return apiError(502, "Subspace API is unavailable.");
  }
}

export { forward as GET, forward as POST, forward as PUT, forward as PATCH, forward as DELETE };
