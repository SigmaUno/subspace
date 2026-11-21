"use client";

import { useCallback, useEffect, useState, type FormEvent } from "react";

export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

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
    throw new ApiError(response.status, payload?.error?.message || `Request failed (HTTP ${response.status}).`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export async function optionalResource<T>(path: string, signal: AbortSignal): Promise<T | null> {
  try {
    return await api<T>(path, "GET", undefined, signal);
  } catch (error) {
    if (error instanceof ApiError && [404, 501].includes(error.status)) return null;
    throw error;
  }
}

function loadResource<T>(path: string, signal: AbortSignal) {
  return api<T>(path, "GET", undefined, signal);
}

export function useResource<T>(path: string | null, revision: number, load = loadResource<T>) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [manual, setManual] = useState(0);
  const refresh = useCallback(() => setManual((count) => count + 1), []);

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

type PageBody = { next_cursor?: string | null } & Record<string, unknown>;

export function usePagedResource<T>(path: string, key: string, revision: number, pageSize = 100) {
  const page = `${path}${path.includes("?") ? "&" : "?"}limit=${pageSize}`;
  const first = useResource<PageBody>(page, revision);
  const [more, setMore] = useState<{ base?: PageBody; items: T[]; next: string | null }>({ items: [], next: null });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const head = (first.data?.[key] as T[] | null | undefined) ?? [];
  const extended = more.base !== undefined && more.base === first.data;
  const items = extended ? [...head, ...more.items] : head;
  const next = extended ? more.next : first.data?.next_cursor ?? null;

  const loadMore = useCallback(async () => {
    const base = first.data;
    if (!base || !next || loading) return;
    setLoading(true);
    try {
      const result = await api<PageBody>(`${page}&cursor=${encodeURIComponent(next)}`);
      const added = (result[key] as T[] | null | undefined) ?? [];
      setMore((previous) => ({
        base,
        items: previous.base === base ? [...previous.items, ...added] : added,
        next: result.next_cursor ?? null,
      }));
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      setLoading(false);
    }
  }, [first.data, next, loading, page, key]);

  return {
    items, loaded: first.data !== undefined, error: first.error || error, loading,
    hasMore: next !== null, loadMore, refresh: first.refresh,
  };
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

export function safeURL(input?: string | null): string | undefined {
  if (!input) return undefined;
  try {
    const url = new URL(input);
    if (url.protocol === "https:" || url.protocol === "http:") return url.href;
  } catch {
  }
}
