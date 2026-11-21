"use client";

import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Prompt, PromptList, RenderedPrompt } from "../../../lib/prompts-types";
import { Feedback } from "./components";
import { useCopy } from "./copy";
import { EmptyState, ResourceState } from "./states";
import { PageShell } from "./shell";

function PromptDetail({ prompt }: { prompt: Prompt }) {
  const [rendered, setRendered] = useState<RenderedPrompt | null>(null);
  const copy = useCopy();
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
      <div className="actions"><button type="button" onClick={() => copy(rendered.text, prompt.title)}>Copy to clipboard</button></div>
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

  return <PageShell section="prompts">
    <div className="heading">
      <h1>Prompts</h1>
      <label className="inline">Tag
        <select value={tag} onChange={(event) => setTag(event.target.value)}>
          <option value="">All tags</option>
          {tags.map((name) => <option key={name} value={name}>{name}</option>)}
        </select>
      </label>
    </div>
    <ResourceState error={all.error || list.error} loading={!list.data} retry={() => { all.refresh(); list.refresh(); }} rows={4} />
    {list.data && !list.data.prompts.length && <EmptyState>No prompts match this tag.</EmptyState>}
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
  </PageShell>;
}
