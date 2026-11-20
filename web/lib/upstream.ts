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

export function browserMutation(request: Request): boolean {
  // A cross-origin form can't supply this header. The API does not enable CORS.
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
