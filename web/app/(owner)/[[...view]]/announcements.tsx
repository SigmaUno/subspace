"use client";

import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Announcement, AnnouncementList, Published } from "../../../lib/announcement-types";
import type { Brief } from "../../../lib/types";
import { Feedback } from "./components";
import { EmptyState, PageHeading, ResourceState } from "./states";
import { useAction, useConfirm, useToast } from "./ui";

const maxText = 2048;

export function AnnouncementsView({ embedded }: { embedded?: boolean }) {
  const list = useResource<AnnouncementList>("announcements", 0);
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=1000", 0);
  const [text, setText] = useState("");
  const remove = useAction();
  const confirm = useConfirm();
  const toast = useToast();
  const form = useSubmit();
  const announcements = list.data?.announcements ?? [];
  const size = new TextEncoder().encode(text).length;

  async function drop(announcement: Announcement) {
    const accepted = await confirm({
      title: `Delete announcement ${announcement.id}?`,
      message: "Agents stop seeing it in their instructions. Messages already posted to their threads stay.",
      confirm: "Delete",
    });
    if (!accepted) return;
    const removed = await remove.run(announcement.id, () => api<void>(`announcements/${encodeURIComponent(announcement.id)}`, "DELETE"),
      `Deleted announcement ${announcement.id}.`);
    if (removed) list.refresh();
  }

  return <>
    <PageHeading title="Announcements" embedded={embedded} />
    <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
      const expires = value(fields, "expires_at");
      const result = await api<Published>("announcements", "POST", {
        text,
        brief_id: value(fields, "brief_id"),
        expires_at: expires ? new Date(expires).toISOString() : null,
      });
      toast(`Published ${result.announcement.id} to ${result.delivered} agent ${result.delivered === 1 ? "thread" : "threads"}.`);
      setText("");
      element.reset();
      list.refresh();
    })}>
      <p className="meta">Announcements are pinned under the agents&apos; instructions and posted to their threads. They are data and cannot grant permissions.</p>
      <label>Text<textarea rows={4} value={text} required onChange={(event) => setText(event.target.value)} /></label>
      <span className={size > maxText ? "error" : "meta"}>{size} / {maxText} bytes</span>
      <div className="form-grid">
        <label>Applies to<select name="brief_id" defaultValue="">
          <option value="">Every agent</option>
          {(briefs.data?.briefs ?? []).map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
        </select></label>
        <label>Expires<input name="expires_at" type="datetime-local" /></label>
      </div>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy || !text.trim() || size > maxText}>Publish announcement</button></div>
    </form>
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={3} />
    {list.data && !announcements.length && <EmptyState>No active announcements.</EmptyState>}
    {!!announcements.length && <section className="panel stack" aria-label="Active announcements">
      {announcements.map((announcement) => <article key={announcement.id} className="stack">
        <div className="heading">
          <strong>{announcement.id}</strong>
          <span className="meta">
            {announcement.brief_id || "every agent"} · {announcement.expires_at
              ? <>expires <time dateTime={announcement.expires_at}>{new Date(announcement.expires_at).toLocaleString()}</time></>
              : "no expiry"}
          </span>
          <button type="button" className="danger" data-write disabled={remove.busy === announcement.id} onClick={() => drop(announcement)}>Delete</button>
        </div>
        <p className="preserve">{announcement.text}</p>
      </article>)}
    </section>}
  </>;
}
