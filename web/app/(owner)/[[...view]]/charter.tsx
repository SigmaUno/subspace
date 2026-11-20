"use client";

import { useEffect, useState } from "react";
import { api, useResource, useSubmit } from "../../../lib/client";
import type { Charter } from "../../../lib/charter-types";
import { Feedback } from "./components";

export function CharterView() {
  const charter = useResource<Charter>("charter", 0);
  const [draft, setDraft] = useState<string>();
  const [saved, setSaved] = useState(false);
  const form = useSubmit();
  const current = charter.data;
  const text = draft ?? current?.text ?? "";
  const dirty = draft !== undefined && draft !== current?.text;

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    function guard(event: MouseEvent) {
      if (event.target instanceof Element && event.target.closest("a") && !window.confirm("Discard unsaved charter changes?")) {
        event.preventDefault();
        event.stopPropagation();
      }
    }
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", guard, true);
    return () => {
      window.removeEventListener("beforeunload", warn);
      document.removeEventListener("click", guard, true);
    };
  }, [dirty]);

  function edit(next: string) {
    setDraft(next);
    setSaved(false);
  }

  return <>
    <div className="heading"><h1>Charter</h1>
      {current?.updated_at && <span className="meta">Last updated <time dateTime={current.updated_at}>{new Date(current.updated_at).toLocaleString()}</time></span>}
    </div>
    <Feedback error={charter.error} loading={!current} />
    {current && <form className="panel stack" onSubmit={(event) => form.submit(event, async () => {
      const response = await api<Charter>("charter", "PUT", { text });
      charter.setData(response);
      setDraft(undefined);
      setSaved(true);
    })}>
      <div className="form-grid">
        <label>Markdown<textarea rows={22} value={text} onChange={(event) => edit(event.target.value)} /></label>
        <div className="stack">
          <span className="meta">Preview</span>
          <div className="preserve">{text || <span className="empty">Nothing yet.</span>}</div>
        </div>
      </div>
      <Feedback error={form.error} />
      {saved && <span className="meta" role="status">Charter saved.</span>}
      <div className="actions">
        <button type="button" disabled={!dirty || form.busy} onClick={() => setDraft(undefined)}>Discard changes</button>
        <button className="primary" disabled={!dirty || form.busy}>Save charter</button>
      </div>
    </form>}
  </>;
}
