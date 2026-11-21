"use client";

import { memo, useCallback, useEffect, useRef, useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Message, Thread } from "../../../lib/types";
import { Feedback } from "./components";

const pageSize = 200;

const MessageItem = memo(function MessageItem({ message }: { message: Message }) {
  return (
    <article>
      <div className="message-meta"><strong>{message.author_id}</strong><span>{message.author_role} · {message.kind}</span>
        <time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString()}</time>
      </div>
      <p className="preserve">{message.body}</p>
    </article>
  );
});

export function ThreadPanel({ thread, revision, top, close }: { thread: Thread; revision: number; top: number; close: () => void }) {
  const path = `threads/${thread.kind}/${encodeURIComponent(thread.id)}/messages`;
  const log = useRef<Message[]>([]);
  const panel = useRef<HTMLElement>(null);
  const messageArea = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const load = useCallback(async (path: string, signal: AbortSignal) => {
    let since = log.current.at(-1)?.id ?? 0;
    const additions: Message[] = [];
    while (true) {
      const result = await api<{ messages: Message[] }>(`${path}?since=${since}&limit=1000`, "GET", undefined, signal);
      const batch = result.messages ?? [];
      additions.push(...batch);
      if (batch.length < 1000) break;
      since = batch[batch.length - 1].id;
    }
    if (!signal.aborted && additions.length) log.current = [...log.current, ...additions];
    return log.current;
  }, []);
  const messages = useResource(path, revision, load);
  const [shown, setShown] = useState(pageSize);
  const hidden = Math.max(0, (messages.data?.length ?? 0) - shown);
  useEffect(() => {
    if (follow.current && messageArea.current) {
      messageArea.current.scrollTop = messageArea.current.scrollHeight;
    }
  }, [messages.data]);
  const form = useSubmit();
  const dismiss = useRef(close);
  useEffect(() => { dismiss.current = close; }, [close]);
  useEffect(() => {
    const previous = document.activeElement;
    panel.current?.focus();
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") dismiss.current(); };
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("keydown", escape);
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);
  return (
    <aside className="thread" aria-label="Thread" tabIndex={-1} ref={panel} style={{ top }}>
      <header className="heading">
        <div><div className="meta">{thread.kind} · {thread.id}</div><h2>{thread.title || thread.id}</h2></div>
        <button onClick={close}>Close</button>
      </header>
      <div className="messages" role="log" aria-label="Messages" tabIndex={0} ref={messageArea}
        onScroll={(event) => {
          const { scrollTop, clientHeight, scrollHeight } = event.currentTarget;
          follow.current = scrollHeight - scrollTop - clientHeight < 48;
        }}>
        <Feedback error={messages.error} loading={!messages.data} />
        {messages.data?.length === 0 && <p className="empty">No messages yet.</p>}
        {hidden > 0 && <button type="button" className="text-button" onClick={() => setShown(shown + pageSize)}>Show {Math.min(hidden, pageSize)} earlier messages ({hidden} hidden)</button>}
        {messages.data?.slice(hidden).map((message) => <MessageItem key={message.id} message={message} />)}
      </div>
      <form className="stack reply" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
        const body = value(fields, "body");
        if (!body) throw new Error("Enter a reply.");
        await api<Message>(path, "POST", { body });
        element.reset();
        messages.refresh();
      })}>
        <label>Owner reply<textarea name="body" required rows={3} /></label>
        <Feedback error={form.error} />
        <div><button className="primary" disabled={form.busy}>Send reply</button></div>
      </form>
    </aside>
  );
}
