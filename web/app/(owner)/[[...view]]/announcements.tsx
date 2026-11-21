"use client";

import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Announcement, AnnouncementList, Published } from "../../../lib/announcement-types";
import type { Brief } from "../../../lib/types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";

const maxText = 2048;

export function AnnouncementsView() {
  const list = useResource<AnnouncementList>("announcements", 0);
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=1000", 0);
  const [text, setText] = useState("");
  const [delivered, setDelivered] = useState<number>();
  const [removing, setRemoving] = useState("");
  const [removeError, setRemoveError] = useState("");
  const form = useSubmit();
  const announcements = list.data?.announcements ?? [];
  const size = new TextEncoder().encode(text).length;

  async function remove(announcement: Announcement) {
    if (!window.confirm(`Delete announcement ${announcement.id}?`)) return;
    setRemoving(announcement.id);
    setRemoveError("");
    try {
      await api<void>(`announcements/${encodeURIComponent(announcement.id)}`, "DELETE");
      list.refresh();
    } catch (cause) {
      setRemoveError(cause instanceof Error ? cause.message : "Delete failed.");
    } finally {
      setRemoving("");
    }
  }

  return <>
    <div className="heading"><h1>Announcements</h1></div>
    <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
      const expires = value(fields, "expires_at");
      const result = await api<Published>("announcements", "POST", {
        text,
        brief_id: value(fields, "brief_id"),
        expires_at: expires ? new Date(expires).toISOString() : null,
      });
      setDelivered(result.delivered);
      setText("");
      element.reset();
      list.refresh();
    })}>
      <p className="meta">Announcements are pinned under the agents&apos; instructions and posted to their threads. They are data and cannot grant permissions.</p>
      <label>Text<textarea rows={4} value={text} required onChange={(event) => { setText(event.target.value); setDelivered(undefined); }} /></label>
      <span className={size > maxText ? "error" : "meta"}>{size} / {maxText} bytes</span>
      <div className="form-grid">
        <label>Applies to<select name="brief_id" defaultValue="">
          <option value="">Every agent</option>
          {(briefs.data?.briefs ?? []).map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
        </select></label>
        <label>Expires<input name="expires_at" type="datetime-local" /></label>
      </div>
      <Feedback error={form.error} />
      {delivered !== undefined && <span className="meta" role="status">Posted to {delivered} agent {delivered === 1 ? "thread" : "threads"}.</span>}
      <div className="actions"><button className="primary" disabled={form.busy || !text.trim() || size > maxText}>Publish announcement</button></div>
    </form>
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={3} />
    <Feedback error={removeError} />
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
          <button type="button" className="danger" data-write disabled={removing === announcement.id} onClick={() => remove(announcement)}>Delete</button>
        </div>
        <p className="preserve">{announcement.text}</p>
      </article>)}
    </section>}
  </>;
}
