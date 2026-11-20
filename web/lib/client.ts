"use client";

import { useCallback, useEffect, useState, type FormEvent } from "react";

export async function api<T>(path: string, method = "GET", body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/v1/${path}`, {
    method, signal,
    headers: { "Content-Type": "application/json", "X-Subspace-Request": "1" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  if (response.status === 401) window.location.replace("/login");
  if (!response.ok) {
    const payload = await response.json().catch(() => null);
    throw new Error(payload?.error?.message || `Request failed (HTTP ${response.status}).`);
  }
  return response.json() as Promise<T>;
}

function loadResource<T>(path: string, signal: AbortSignal) {
  return api<T>(path, "GET", undefined, signal);
}

export function useResource<T>(path: string | null, revision: number, load = loadResource<T>) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [manual, setManual] = useState(0);
  const refresh = useCallback(() => setManual((count) => count + 1), []);

  // Finish an in-flight snapshot before starting another. Under sustained SSE
  // traffic, aborting on every invalidation could prevent a slow read completing.
  const [request, setRequest] = useState({ path, revision, manual });
  const [completed, setCompleted] = useState<typeof request>();
  if (completed === request &&
    (request.path !== path || request.revision !== revision || request.manual !== manual)) {
    setRequest({ path, revision, manual });
  }

  useEffect(() => {
    const controller = new AbortController();
    if (!request.path) {
      queueMicrotask(() => { if (!controller.signal.aborted) setCompleted(request); });
    } else {
      load(request.path, controller.signal)
        .then((value) => { setData(value); setError(""); })
        .catch((cause: Error) => { if (!controller.signal.aborted) setError(cause.message); })
        .finally(() => { if (!controller.signal.aborted) setCompleted(request); });
    }
    return () => controller.abort();
  }, [request, load]);

  useEffect(() => {
    const timer = window.setInterval(refresh, 30_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  return { data, error, refresh, setData };
}

export function useSubmit() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>, run: (form: FormData, element: HTMLFormElement) => Promise<void>) {
    event.preventDefault();
    const form = event.currentTarget;
    setBusy(true);
    setError("");
    try {
      await run(new FormData(form), form);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      setBusy(false);
    }
  }

  return { busy, error, submit };
}

export function value(form: FormData, key: string): string {
  return String(form.get(key) ?? "").trim();
}

export function commaList(input: string) {
  return input.split(/[,\n]/).map((entry) => entry.trim()).filter(Boolean);
}

const euro = new Intl.NumberFormat("en", { style: "currency", currency: "EUR" });
export const money = (amount: number) => euro.format(amount);
export const label = (status: string) => status.replaceAll("_", " ");

export function safeURL(input: string | null): string | undefined {
  if (!input) return undefined;
  try {
    const url = new URL(input);
    if (url.protocol === "https:" || url.protocol === "http:") return url.href;
  } catch {
    // Treat an invalid link as plain data.
  }
}
