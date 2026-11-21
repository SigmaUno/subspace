"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { api, money, useResource, useSubmit, value } from "../../../../lib/client";
import type { BriefImport, BriefPreview, BriefTemplate, RenderedBriefTemplate } from "../../../../lib/brief-import-types";
import { Feedback } from "../shared/components";
import { PlaybookPicker, usePlaybooks } from "../playbooks";

type Check = { text: string; preview?: BriefPreview; error?: string };

function frontmatterVariables(template: BriefTemplate) {
  const head = template.markdown.split("\n---\n")[0];
  return new Set(template.variables.filter((name) => head.includes(`{{${name}}}`)));
}

function TemplateForm({ template, fill }: { template: BriefTemplate; fill: (markdown: string) => void }) {
  const form = useSubmit();
  const singleLine = frontmatterVariables(template);
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
    const params = new URLSearchParams();
    for (const name of template.variables) params.set(name, value(fields, name));
    const rendered = await api<RenderedBriefTemplate>(`brief-templates/${encodeURIComponent(template.id)}/render?${params}`);
    fill(rendered.markdown);
  })}>
    <div className="form-grid">
      {template.variables.map((name) => <label key={name}>{name}
        {singleLine.has(name) ? <input name={name} required /> : <textarea name={name} rows={2} required />}
      </label>)}
    </div>
    <Feedback error={form.error} />
    <div><button type="submit" disabled={form.busy}>Fill document</button></div>
  </form>;
}

function withPlaybook(markdown: string, name: string) {
  if (!name) return markdown;
  const lines = markdown.split("\n");
  if (lines[0].trim() !== "---") return markdown;
  const end = lines.findIndex((line, index) => index > 0 && line.trim() === "---");
  if (end < 0) return markdown;
  const head = lines.slice(1, end).filter((line) => !/^playbook\s*:/.test(line));
  return ["---", `playbook: ${name}`, ...head, ...lines.slice(end)].join("\n");
}

function Preview({ preview }: { preview: BriefPreview }) {
  return <div className="stack">
    <h3>{preview.title}</h3>
    <span className="meta">{preview.repos.join(", ") || "No repositories"}</span>
    <span className="meta">
      {money(preview.max_cost_eur)} · {preview.max_parallel_agents} parallel agents · {preview.allowed_providers.join(", ") || "All providers"} · Cross review {preview.cross_review ? "on" : "off"}
    </span>
    {preview.playbook && <span className="meta">Playbook {preview.playbook}</span>}
    {preview.task_budget && <span className="meta">
      Task defaults: {preview.task_budget.max_minutes} min · {money(preview.task_budget.max_cost_eur)}
    </span>}
    {!!preview.components.length && <ul>
      {preview.components.map((component) => <li key={component.name}>
        <strong>{component.name}</strong>
        {component.depends_on.length > 0 && <span className="meta"> → {component.depends_on.join(", ")}</span>}
        {component.paths.length > 0 && <div className="meta">{component.paths.join(", ")}</div>}
      </li>)}
    </ul>}
    <pre className="preserve">{preview.body || "No body"}</pre>
  </div>;
}

export function ImportBrief() {
  const router = useRouter();
  const templates = useResource<{ templates: BriefTemplate[] }>("brief-templates", 0);
  const [templateID, setTemplateID] = useState("");
  const [markdown, setMarkdown] = useState("");
  const [playbook, setPlaybook] = useState("");
  const playbooks = usePlaybooks();
  const [check, setCheck] = useState<Check>({ text: "" });
  const create = useSubmit();
  const template = templates.data?.templates.find((entry) => entry.id === templateID);
  const source = withPlaybook(markdown, playbook);

  useEffect(() => {
    if (!source.trim()) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api<{ preview: BriefPreview }>("briefs/import?dry_run=true", "POST", { markdown: source }, controller.signal)
        .then((result) => setCheck({ text: source, preview: result.preview }))
        .catch((cause: Error) => { if (!controller.signal.aborted) setCheck({ text: source, error: cause.message }); });
    }, 300);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [source]);

  const current = markdown.trim() && check.text === source ? check : null;

  return <section className="panel stack">
    <h2>Import brief</h2>
    <label>Template
      <select value={templateID} onChange={(event) => setTemplateID(event.target.value)}>
        <option value="">No template</option>
        {templates.data?.templates.map((entry) => <option key={entry.id} value={entry.id}>{entry.title}</option>)}
      </select>
    </label>
    <Feedback error={templates.error} />
    {!!playbooks.list.length && <PlaybookPicker playbooks={playbooks.list} selected={playbook} onChange={setPlaybook} />}
    {template && <TemplateForm key={template.id} template={template} fill={setMarkdown} />}
    <form className="stack" onSubmit={(event) => create.submit(event, async () => {
      const result = await api<BriefImport>("briefs/import", "POST", { markdown: source });
      router.push(`/briefs/${encodeURIComponent(result.brief.id)}`);
    })}>
      <label>Brief document
        <textarea rows={16} spellCheck={false} value={markdown} onChange={(event) => setMarkdown(event.target.value)}
          placeholder={"---\ntitle: \"...\"\nrepos: [owner/repo]\n---\nBrief body"} />
      </label>
      <section className="stack" aria-live="polite">
        <h3>Validation</h3>
        {!markdown.trim() && <p className="muted">Paste a brief document or fill one from a template.</p>}
        {markdown.trim() && !current && <p className="muted" role="status">Checking…</p>}
        {current?.error && <p className="error" role="alert">{current.error}</p>}
        {current?.preview && <Preview preview={current.preview} />}
      </section>
      <Feedback error={create.error} />
      <div><button type="submit" className="primary" disabled={create.busy || !current?.preview}>Import brief</button></div>
    </form>
  </section>;
}
