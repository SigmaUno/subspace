"use client";

import { useRef, useState, type DragEvent } from "react";
import { api, useResource } from "../../../lib/client";
import type { Artifact, ArtifactList, ScopeKind, ScopeOptions } from "../../../lib/files-types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";

const kinds: { id: ScopeKind; name: string; resource: keyof ScopeOptions; prefix: string }[] = [
  { id: "agent", name: "Agent", resource: "agents", prefix: "agent" },
  { id: "task", name: "Task", resource: "tasks", prefix: "task" },
  { id: "brief", name: "Brief", resource: "briefs", prefix: "project" },
];

type Transfer = { key: number; name: string; percent: number; error: string; done: boolean };

const units = ["B", "KB", "MB", "GB"];

function formatSize(bytes: number) {
  let size = bytes;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${unit ? size.toFixed(1) : size} ${units[unit]}`;
}

function send(scope: string, file: File, progress: (percent: number) => void) {
  return new Promise<void>((resolve, reject) => {
    const body = new FormData();
    body.append("scope", scope);
    body.append("name", file.name);
    body.append("file", file);
    const request = new XMLHttpRequest();
    request.open("POST", "/api/v1/artifacts");
    request.setRequestHeader("X-Subspace-Request", "1");
    request.upload.onprogress = (event) => {
      if (event.lengthComputable) progress(Math.round(event.loaded / event.total * 100));
    };
    request.onerror = () => reject(new Error("Upload failed."));
    request.onload = () => {
      if (request.status === 401) window.location.replace("/login");
      if (request.status >= 200 && request.status < 300) return resolve();
      let message = `Upload failed (HTTP ${request.status}).`;
      try {
        message = JSON.parse(request.responseText).error?.message || message;
      } catch {
      }
      reject(new Error(message));
    };
    request.send(body);
  });
}

function ScopeFiles({ scope }: { scope: string }) {
  const files = useResource<ArtifactList>(`artifacts?${new URLSearchParams({ scope, limit: "1000" })}`, 0);
  const [transfers, setTransfers] = useState<Transfer[]>([]);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState("");
  const counter = useRef(0);
  const input = useRef<HTMLInputElement>(null);

  function update(key: number, change: Partial<Transfer>) {
    setTransfers((current) => current.map((item) => item.key === key ? { ...item, ...change } : item));
  }

  async function upload(selected: FileList | null) {
    const batch = Array.from(selected ?? []);
    if (!batch.length) return;
    const started = batch.map((file) => ({ key: ++counter.current, file }));
    setTransfers((current) => [
      ...current.filter((item) => !item.done),
      ...started.map(({ key, file }) => ({ key, name: file.name, percent: 0, error: "", done: false })),
    ]);
    await Promise.all(started.map(({ key, file }) =>
      send(scope, file, (percent) => update(key, { percent }))
        .then(() => update(key, { percent: 100, done: true }))
        .catch((cause: Error) => update(key, { error: cause.message, done: true }))));
    files.refresh();
  }

  function drop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    void upload(event.dataTransfer.files);
  }

  async function remove(artifact: Artifact) {
    if (!window.confirm(`Delete ${artifact.name}?`)) return;
    setRemoving(artifact.id);
    setError("");
    try {
      await api<void>(`artifacts/${encodeURIComponent(artifact.id)}`, "DELETE");
      files.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Delete failed.");
    } finally {
      setRemoving("");
    }
  }

  return <>
    <div className="empty stack" style={dragging ? { borderColor: "var(--action)" } : undefined}
      onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
      onDragLeave={() => setDragging(false)}
      onDrop={drop}>
      <p>Drop files here to upload to {scope}</p>
      <div className="actions" data-write>
        <button type="button" onClick={() => input.current?.click()}>Choose files</button>
        <input ref={input} type="file" multiple hidden aria-label="Files to upload"
          onChange={(event) => { void upload(event.target.files); event.target.value = ""; }} />
      </div>
    </div>
    {!!transfers.length && <ul className="stack" aria-label="Uploads">
      {transfers.map((item) => <li key={item.key} className="progress">
        <span>{item.name}</span>
        {item.error
          ? <span className="error" role="alert">{item.error}</span>
          : <><progress max={100} value={item.percent} aria-label={`Uploading ${item.name}`} /><span>{item.percent}%</span></>}
      </li>)}
    </ul>}
    <Feedback error={error} />
    <ResourceState error={files.error} loading={!files.data} retry={files.refresh} rows={4} />
    {files.data && !files.data.artifacts.length && <EmptyState>No files in this scope.</EmptyState>}
    {!!files.data?.artifacts.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Type</th><th>Size</th><th>Uploaded</th><th>Actions</th></tr></thead>
        <tbody>
          {files.data.artifacts.map((artifact) => <tr key={artifact.id}>
            <th scope="row">{artifact.name}<div className="meta">{artifact.id} · {artifact.uploaded_by}</div></th>
            <td>{artifact.content_type}</td>
            <td>{formatSize(artifact.size)}</td>
            <td><time dateTime={artifact.created_at}>{new Date(artifact.created_at).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}</time></td>
            <td><div className="actions">
              <a href={`/api/v1/artifacts/${encodeURIComponent(artifact.id)}`} download={artifact.name}>Download</a>
              <button type="button" className="danger" data-write disabled={removing === artifact.id} onClick={() => remove(artifact)}>Delete</button>
            </div></td>
          </tr>)}
        </tbody>
      </table>
    </div>}
  </>;
}

export function FilesView() {
  const [choice, setChoice] = useState<{ kind: ScopeKind; id: string }>({ kind: "agent", id: "" });
  const kind = kinds.find((item) => item.id === choice.kind) ?? kinds[0];
  const options = useResource<ScopeOptions>(kind.resource, 0);
  const list: { id: string; title?: string }[] | undefined = options.data?.[kind.resource];
  const items = list ?? [];
  const id = items.some((item) => item.id === choice.id) ? choice.id : items[0]?.id ?? "";

  return <>
    <div className="heading">
      <h1>Files</h1>
      <div className="actions">
        <label className="inline">Scope
          <select value={kind.id} onChange={(event) => setChoice({ kind: event.target.value as ScopeKind, id: "" })}>
            {kinds.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
        </label>
        <label className="inline">{kind.name}
          <select value={id} onChange={(event) => setChoice({ kind: kind.id, id: event.target.value })} disabled={!items.length}>
            {items.map((item) => <option key={item.id} value={item.id}>{item.title ? `${item.id} · ${item.title}` : item.id}</option>)}
          </select>
        </label>
      </div>
    </div>
    <ResourceState error={options.error} loading={!list} retry={options.refresh} rows={2} />
    {list && !items.length && <EmptyState>No {kind.resource} yet.</EmptyState>}
    {id && <ScopeFiles key={`${kind.prefix}:${id}`} scope={`${kind.prefix}:${id}`} />}
  </>;
}
