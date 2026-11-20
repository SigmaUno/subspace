"use client";

import Link from "next/link";
import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Prompt, PromptList, RenderedPrompt } from "../../../lib/prompts-types";
import { Feedback } from "./components";

function CopyButton({ text }: { text: string }) {
  const [feedback, setFeedback] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setFeedback("Copied");
    } catch {
      setFeedback("Select and copy the text.");
    }
  }
  return <div className="actions">
    <button type="button" onClick={copy}>Copy to clipboard</button>
    {feedback && <span className="meta" role="status">{feedback}</span>}
  </div>;
}

function PromptDetail({ prompt }: { prompt: Prompt }) {
  const [rendered, setRendered] = useState<RenderedPrompt | null>(null);
  const form = useSubmit();
  return <section className="panel stack">
    <div className="heading">
      <div><h2>{prompt.title}</h2><p className="meta">{prompt.id}</p></div>
      <div className="actions">{prompt.tags.map((tag) => <span key={tag} className="pill">{tag}</span>)}</div>
    </div>
    <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
      const params = new URLSearchParams();
      for (const name of prompt.variables) params.set(name, value(fields, name));
      setRendered(await api<RenderedPrompt>(`prompts/${encodeURIComponent(prompt.id)}/render?${params}`));
    })}>
      {!!prompt.variables.length && <div className="form-grid">
        {prompt.variables.map((name) => <label key={name}>{name}<input name={name} required /></label>)}
      </div>}
      <Feedback error={form.error} />
      <div><button className="primary" disabled={form.busy}>Render preview</button></div>
    </form>
    {rendered && <div className="stack">
      <h2>Preview</h2>
      <pre>{rendered.text}</pre>
      <CopyButton text={rendered.text} />
    </div>}
  </section>;
}

export function PromptsPage() {
  const [tag, setTag] = useState("");
  const [selected, setSelected] = useState("");
  const all = useResource<PromptList>("prompts", 0);
  const list = useResource<PromptList>(tag ? `prompts?${new URLSearchParams({ tag })}` : "prompts", 0);
  const tags = [...new Set(all.data?.prompts.flatMap((prompt) => prompt.tags) ?? [])].sort();
  const current = list.data?.prompts.find((prompt) => prompt.id === selected);

  return <>
    <header className="topbar">
      <Link className="brand" href="/briefs"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
      <nav aria-label="Workspace">
        <Link href="/briefs">Briefs</Link>
        <Link href="/prompts" aria-current="page">Prompts</Link>
      </nav>
    </header>
    <main className="workspace">
      <div className="heading">
        <h1>Prompts</h1>
        <label className="inline">Tag
          <select value={tag} onChange={(event) => setTag(event.target.value)}>
            <option value="">All tags</option>
            {tags.map((name) => <option key={name} value={name}>{name}</option>)}
          </select>
        </label>
      </div>
      <Feedback error={all.error || list.error} loading={!list.data} />
      {list.data && !list.data.prompts.length && <p className="empty">No prompts match this tag.</p>}
      {!!list.data?.prompts.length && <div className="table-wrap">
        <table>
          <thead><tr><th>Prompt</th><th>Tags</th><th>Variables</th></tr></thead>
          <tbody>{list.data.prompts.map((prompt) => (
            <tr key={prompt.id}>
              <th scope="row"><button className="text-button" aria-pressed={prompt.id === selected}
                onClick={() => setSelected(prompt.id)}>{prompt.title}</button>
                <div className="meta">{prompt.id}</div></th>
              <td>{prompt.tags.join(", ") || "—"}</td>
              <td>{prompt.variables.join(", ") || "—"}</td>
            </tr>
          ))}</tbody>
        </table>
      </div>}
      {current && <PromptDetail key={current.id} prompt={current} />}
    </main>
  </>;
}
