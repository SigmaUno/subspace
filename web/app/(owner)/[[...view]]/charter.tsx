"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { api, useResource, useSubmit } from "../../../lib/client";
import type { Charter } from "../../../lib/charter-types";
import { Feedback } from "./components";
import { PageHeading, ResourceState } from "./states";
import { useConfirm, useToast } from "./ui";

export function CharterView({ embedded }: { embedded?: boolean }) {
  const charter = useResource<Charter>("charter", 0);
  const [draft, setDraft] = useState<string>();
  const form = useSubmit();
  const router = useRouter();
  const confirm = useConfirm();
  const toast = useToast();
  const current = charter.data;
  const text = draft ?? current?.text ?? "";
  const dirty = draft !== undefined && draft !== current?.text;

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    function guard(event: MouseEvent) {
      const link = event.target instanceof Element ? event.target.closest("a") : null;
      if (!link || link.target === "_blank" || link.hasAttribute("download") || event.ctrlKey || event.metaKey || event.shiftKey) return;
      event.preventDefault();
      event.stopPropagation();
      void confirm({
        title: "Discard unsaved charter changes?", message: "Your edits to the charter are lost.", confirm: "Discard",
      }).then((accepted) => {
        if (!accepted) return;
        if (link.origin === window.location.origin) router.push(`${link.pathname}${link.search}${link.hash}`);
        else window.location.assign(link.href);
      });
    }
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", guard, true);
    return () => {
      window.removeEventListener("beforeunload", warn);
      document.removeEventListener("click", guard, true);
    };
  }, [dirty, confirm, router]);

  function edit(next: string) {
    setDraft(next);
  }

  return <>
    <PageHeading title="Charter" embedded={embedded}>
      {current?.updated_at && <span className="meta">Last updated <time dateTime={current.updated_at}>{new Date(current.updated_at).toLocaleString()}</time></span>}
    </PageHeading>
    <ResourceState error={charter.error} loading={!current} retry={charter.refresh} rows={6} />
    {current && <form className="panel stack" onSubmit={(event) => form.submit(event, async () => {
      const response = await api<Charter>("charter", "PUT", { text });
      charter.setData(response);
      setDraft(undefined);
      toast("Charter saved.");
    })}>
      <div className="form-grid">
        <label data-write>Markdown<textarea rows={22} value={text} onChange={(event) => edit(event.target.value)} /></label>
        <div className="stack">
          <span className="meta">Preview</span>
          <div className="preserve">{text || <span className="empty">Nothing yet.</span>}</div>
        </div>
      </div>
      <Feedback error={form.error} />
      <div className="actions" data-write>
        <button type="button" disabled={!dirty || form.busy} onClick={() => setDraft(undefined)}>Discard changes</button>
        <button className="primary" disabled={!dirty || form.busy}>Save charter</button>
      </div>
    </form>}
  </>;
}
