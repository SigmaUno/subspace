import { ApiError } from "./errors.ts";
import { parseSse } from "./sse.ts";
import type { SseMessage } from "./sse.ts";

export interface ReconnectOptions {
  initialDelayMs?: number;
  maxDelayMs?: number;
  maxConsecutiveFailures?: number;
}

export interface FollowOptions {
  lastEventId?: string;
  signal?: AbortSignal;
  reconnect?: false | ReconnectOptions;
}

export type OpenStream = (lastEventId: string | undefined, signal: AbortSignal | undefined) => Promise<Response>;

function retryable(err: unknown): boolean {
  if (err instanceof ApiError) {
    return err.status >= 500 || err.status === 408 || err.status === 429;
  }
  return err instanceof TypeError;
}

function aborted(signal: AbortSignal | undefined): boolean {
  return signal?.aborted === true;
}

function sleep(ms: number, signal: AbortSignal | undefined): Promise<void> {
  return new Promise((resolve) => {
    if (aborted(signal)) return resolve();
    const done = () => {
      clearTimeout(timer);
      signal?.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal?.addEventListener("abort", done, { once: true });
  });
}

export async function* follow(open: OpenStream, options: FollowOptions = {}): AsyncGenerator<SseMessage> {
  const { signal } = options;
  const policy = options.reconnect === false ? undefined : (options.reconnect ?? {});
  const initial = policy?.initialDelayMs ?? 500;
  const ceiling = policy?.maxDelayMs ?? 30_000;
  const budget = policy?.maxConsecutiveFailures ?? Infinity;
  let lastId = options.lastEventId;
  let failures = 0;
  let serverDelay: number | undefined;

  while (!aborted(signal)) {
    let received = false;
    try {
      const res = await open(lastId, signal);
      if (res.body === null) throw new TypeError("event stream has no body");
      for await (const msg of parseSse(res.body)) {
        received = true;
        if (msg.id !== undefined) lastId = msg.id;
        if (msg.retry !== undefined) serverDelay = msg.retry;
        yield msg;
        if (aborted(signal)) return;
      }
    } catch (err) {
      if (aborted(signal)) return;
      if (policy === undefined || !retryable(err)) throw err;
      failures += 1;
      if (failures > budget) throw err;
    }
    if (aborted(signal)) return;
    if (policy === undefined) return;
    if (received) failures = 0;
    const base = serverDelay ?? initial;
    const delay = Math.min(ceiling, base * 2 ** Math.max(0, failures - 1));
    await sleep(delay * (0.8 + Math.random() * 0.4), signal);
  }
}
