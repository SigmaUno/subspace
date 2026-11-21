import "server-only";

export const ownerCookie = "subspace_owner";

export function upstream(path: string, token: string, options: RequestInit = {}) {
  const base = new URL(process.env.SUBSPACE_API_URL || "http://server:8080");
  const headers = new Headers(options.headers);
  headers.set("Authorization", `Bearer ${token}`);
  return fetch(new URL(path, base), {
    ...options,
    headers,
    cache: "no-store",
    redirect: "error",
  });
}

export type OwnerSession = { status: number; scope?: string };

export async function ownerSession(token: string, signal?: AbortSignal): Promise<OwnerSession> {
  const timeout = AbortSignal.timeout(8000);
  const response = await upstream("/api/v1/me", token, {
    signal: signal ? AbortSignal.any([timeout, signal]) : timeout,
  });
  if (!response.ok) {
    await response.body?.cancel();
    return { status: response.status };
  }
  const body: { scope?: unknown } | null = await response.json().catch(() => null);
  return { status: response.status, scope: typeof body?.scope === "string" ? body.scope : undefined };
}

export async function ownerStatus(token: string, signal?: AbortSignal) {
  return (await ownerSession(token, signal)).status;
}

export function browserMutation(request: Request): boolean {
  return request.headers.get("x-subspace-request") === "1" &&
    !["cross-site", "same-site"].includes(request.headers.get("sec-fetch-site") ?? "");
}

export function apiError(status: number, message: string) {
  return Response.json({ error: { message } }, {
    status,
    headers: {
      "Cache-Control": "no-store",
      ...(status === 401 ? { "WWW-Authenticate": "Bearer" } : {}),
    },
  });
}
