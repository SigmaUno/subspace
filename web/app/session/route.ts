import { NextRequest, NextResponse } from "next/server";
import { apiError, browserMutation, ownerCookie, ownerStatus } from "../../lib/upstream";

function protocol(request: NextRequest) {
  const forwarded = request.headers.get("x-forwarded-proto")?.split(",")[0]?.trim();
  return forwarded === "https" || forwarded === "http" ? `${forwarded}:` : request.nextUrl.protocol;
}

function cookieOptions(request: NextRequest) {
  return {
    httpOnly: true,
    sameSite: "strict" as const,
    path: "/",
    secure: protocol(request) === "https:",
  };
}

function sameOriginForm(request: NextRequest) {
  const fetchSite = request.headers.get("sec-fetch-site");
  if (fetchSite && fetchSite !== "same-origin") return false;
  const origin = request.headers.get("origin");
  const host = request.headers.get("host");
  if (!origin || !host) return false;
  try {
    return new URL(`${protocol(request)}//${host}`).origin === origin;
  } catch {
    return false;
  }
}

function redirectTo(location: string) {
  return new NextResponse(null, { status: 303, headers: { Location: location, "Cache-Control": "no-store" } });
}

export async function POST(request: NextRequest) {
  if (!sameOriginForm(request)) return apiError(403, "Same-origin request required.");

  const fields = await request.formData().catch(() => null);
  const input = fields?.get("token");
  const token = typeof input === "string" ? input.trim() : "";
  if (!token || /[^\x21-\x7e]/.test(token)) return redirectTo("/login?error=invalid");

  try {
    const status = await ownerStatus(token, request.signal);
    if (status === 401 || status === 403) return redirectTo("/login?error=rejected");
    if (status !== 200) return redirectTo("/login?error=unavailable");
  } catch {
    return redirectTo("/login?error=unavailable");
  }

  const response = redirectTo("/");
  response.cookies.set(ownerCookie, token, cookieOptions(request));
  return response;
}

export function DELETE(request: NextRequest) {
  if (!browserMutation(request)) return apiError(403, "Same-origin request required.");
  const response = new NextResponse(null, { status: 204, headers: { "Cache-Control": "no-store" } });
  response.cookies.set(ownerCookie, "", { ...cookieOptions(request), maxAge: 0 });
  return response;
}
