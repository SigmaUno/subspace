"use client";

import { api, usePagedResource, useSubmit, value } from "../../../../lib/client";
import type { TimelineEntry } from "../../../../lib/task-detail-types";
import type { Message } from "../../../../lib/types";
import { Feedback, LoadMore } from "../shared/components";
import { stamp } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";
import { useToast } from "../shared/ui";

type Paged<T> = ReturnType<typeof usePagedResource<T>>;

export function TaskTimeline({ audit, entries }: { audit: Paged<unknown>; entries: TimelineEntry[] }) {
  return <section className="panel stack">
    <h2>Timeline</h2>
    <ResourceState error={audit.error} loading={!audit.loaded && !audit.error} retry={audit.refresh} />
    {audit.loaded && !entries.length && <EmptyState>No activity yet.</EmptyState>}
    <div className="messages">
      {entries.map((entry) => (
        <article key={entry.key}>
          <div className="message-meta"><strong>{entry.title}</strong><span>{entry.actor}</span>
            <time dateTime={entry.at}>{stamp(entry.at)}</time>
          </div>
          {entry.detail && <p className="preserve">{entry.detail}</p>}
        </article>
      ))}
      <LoadMore hasMore={audit.hasMore} loading={audit.loading} onMore={audit.loadMore} />
    </div>
  </section>;
}

export function TaskThread({ path, messages }: { path: string; messages: Paged<Message> }) {
  const reply = useSubmit();
  const toast = useToast();
  const log = messages.items;
  return <section className="panel stack">
    <h2>Thread</h2>
    <ResourceState error={messages.error} loading={!messages.loaded && !messages.error} retry={messages.refresh} />
    {messages.loaded && !log.length && <EmptyState>No messages yet.</EmptyState>}
    <div className="messages" role="log" aria-label="Messages">
      {log.map((message) => (
        <article key={message.id}>
          <div className="message-meta"><strong>{message.author_id}</strong><span>{message.author_role} · {message.kind}</span>
            <time dateTime={message.created_at}>{stamp(message.created_at)}</time>
          </div>
          <p className="preserve">{message.body}</p>
        </article>
      ))}
      <LoadMore hasMore={messages.hasMore} loading={messages.loading} onMore={messages.loadMore} />
    </div>
    <form className="stack" data-write onSubmit={(event) => reply.submit(event, async (fields, element) => {
      const body = value(fields, "body");
      if (!body) throw new Error("Enter a reply.");
      await api(`threads/task/${path}/messages`, "POST", { body });
      toast("Reply sent.");
      element.reset();
      messages.refresh();
    })}>
      <label>Owner reply<textarea name="body" required rows={3} /></label>
      <Feedback error={reply.error} />
      <div><button className="primary" disabled={reply.busy}>Send reply</button></div>
    </form>
  </section>;
}
