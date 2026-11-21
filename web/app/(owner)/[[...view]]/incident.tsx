"use client";

import { useEffect, useId, useRef, useState } from "react";
import { api, useResource, useSubmit } from "../../../lib/client";
import type { Incident, IncidentEnded, IncidentList, IncidentStarted } from "../../../lib/incident-types";
import { Feedback } from "./components";
import "./incident.css";

const changed = "subspace:incident";

const roles = ["leader", "worker", "tester", "reviewer"];

const options = [
  { id: "pause_all", name: "Pause everything" },
  { id: "revoke_agents", name: "Revoke agents" },
  { id: "rotate_leader_tokens", name: "Rotate leader tokens" },
  { id: "snapshot", name: "Save a state snapshot" },
  { id: "notify", name: "Send a notification" },
];

function effect(id: string, role: string, provider: string) {
  switch (id) {
    case "pause_all": return "All leases are revoked and claims and spawns are blocked until the incident ends.";
    case "revoke_agents": {
      const scope = [role && `role ${role}`, provider && `provider ${provider}`].filter(Boolean).join(" and ");
      return `${scope ? `Agents with ${scope}` : "Every agent"} lose their token immediately and release their leases. This cannot be undone.`;
    }
    case "rotate_leader_tokens": return "Every active leader gets a new token, shown once below. The old tokens stop working.";
    case "snapshot": return "Briefs, agents and tasks are written to the server's data directory.";
    default: return "A summary goes to the configured notification channels.";
  }
}

function useIncidents(revision: number) {
  const list = useResource<IncidentList>("incident?limit=20", revision);
  const { refresh } = list;
  useEffect(() => {
    window.addEventListener(changed, refresh);
    return () => window.removeEventListener(changed, refresh);
  }, [refresh]);
  return { ...list, active: list.data?.incidents?.find((item) => !item.ended_at) };
}

export function IncidentBanner({ revision }: { revision: number }) {
  const { active } = useIncidents(revision);
  const [resumeAgents, setResumeAgents] = useState(false);
  const form = useSubmit();
  if (!active) return null;
  return <section className="incident-banner" role="alert">
    <div>
      <strong>Incident {active.id} is active</strong>
      <p>{active.reason} · since {new Date(active.started_at).toLocaleString()} · {active.actions.join(", ")}</p>
    </div>
    <form className="actions" data-write onSubmit={(event) => form.submit(event, async () => {
      if (!window.confirm(`End incident ${active.id}? Everything is resumed${resumeAgents ? " and paused agents are re-enabled" : ""}. Revoked agents stay revoked.`)) return;
      await api<IncidentEnded>(`incident/${encodeURIComponent(active.id)}/end`, "POST", { resume_agents: resumeAgents });
      window.dispatchEvent(new Event(changed));
    })}>
      <label><input type="checkbox" checked={resumeAgents} onChange={(event) => setResumeAgents(event.target.checked)} />Re-enable paused agents</label>
      <button disabled={form.busy}>End incident</button>
    </form>
    <Feedback error={form.error} />
  </section>;
}

export function IncidentButton({ revision }: { revision: number }) {
  const { active } = useIncidents(revision);
  const [open, setOpen] = useState(false);
  return <>
    <button type="button" className="incident-button" data-write onClick={() => setOpen(true)}>{active ? "Incident active" : "Incident"}</button>
    {open && <IncidentDialog active={active} close={() => setOpen(false)} />}
  </>;
}

function IncidentDialog({ active, close }: { active?: Incident; close: () => void }) {
  const titleID = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const onClose = useRef(close);
  const [reason, setReason] = useState(active?.reason ?? "");
  const [actions, setActions] = useState(["pause_all", "snapshot", "notify"]);
  const [role, setRole] = useState("");
  const [provider, setProvider] = useState("");
  const [outcome, setOutcome] = useState<IncidentStarted>();
  const form = useSubmit();

  useEffect(() => {
    onClose.current = close;
  });

  useEffect(() => {
    const returnFocus = document.activeElement;
    dialog.current?.focus();
    function onEscape(event: KeyboardEvent) {
      if (event.key === "Escape") onClose.current();
    }
    window.addEventListener("keydown", onEscape);
    return () => {
      window.removeEventListener("keydown", onEscape);
      if (returnFocus instanceof HTMLElement) returnFocus.focus();
    };
  }, []);

  const toggle = (id: string, on: boolean) => setActions((current) => on ? [...current, id] : current.filter((item) => item !== id));
  const revoking = actions.includes("revoke_agents");
  const scoped = revoking && (role !== "" || provider.trim() !== "");

  return <div className="dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}>
    <div role="dialog" aria-modal="true" aria-labelledby={titleID} tabIndex={-1} ref={dialog} className="panel stack incident-dialog">
      <div className="heading"><h2 id={titleID}>{active ? `Add to incident ${active.id}` : "Declare an incident"}</h2>
        <button type="button" onClick={close}>Close</button></div>
      {outcome ? <>
        <p><strong>Incident {outcome.id}</strong> · {outcome.succeeded} done, {outcome.failed} failed</p>
        <ul>
          {outcome.results.map((result) => <li key={result.action}>
            <span className={`pill ${result.status === "ok" ? "active" : "revoked"}`}>{result.status}</span> {result.action}
            {result.agents?.length ? ` · ${result.agents.length} agents` : ""}
            {result.error && ` · ${result.error}`}
            {result.failures?.map((failure) => <div key={failure.agent_id} className="stale">{failure.agent_id}: {failure.error}</div>)}
          </li>)}
        </ul>
        {!!outcome.tokens?.length && <div className="notice" role="status">
          <strong>New leader tokens. They are shown once.</strong>
          <ul>{outcome.tokens.map((item) => <li key={item.agent_id}>{item.agent_id}: <code>{item.token}</code></li>)}</ul>
        </div>}
        <div className="actions"><button type="button" className="primary" onClick={close}>Done</button></div>
      </> : <form className="stack" onSubmit={(event) => form.submit(event, async () => {
        const body = {
          reason: reason.trim(), actions,
          ...(scoped ? { revoke: { role, provider: provider.trim() } } : {}),
        };
        setOutcome(await api<IncidentStarted>("incident", "POST", body));
        window.dispatchEvent(new Event(changed));
      })}>
        <label>Reason
          <input value={reason} onChange={(event) => setReason(event.target.value)} maxLength={500} required disabled={!!active} />
        </label>
        <fieldset disabled={form.busy}>
          <legend>Effects</legend>
          {options.map((option) => <label key={option.id} className="incident-choice">
            <input type="checkbox" checked={actions.includes(option.id)} onChange={(event) => toggle(option.id, event.target.checked)} />
            <strong>{option.name}</strong> <span className="meta">{effect(option.id, role, provider.trim())}</span>
          </label>)}
        </fieldset>
        {revoking && <div className="actions">
          <label>Revoke role
            <select value={role} onChange={(event) => setRole(event.target.value)}>
              <option value="">All roles</option>{roles.map((item) => <option key={item} value={item}>{item}</option>)}
            </select>
          </label>
          <label>Revoke provider
            <input value={provider} placeholder="All providers" onChange={(event) => setProvider(event.target.value)} />
          </label>
        </div>}
        <Feedback error={form.error} />
        <div className="actions">
          <button className="incident-button" disabled={form.busy || !actions.length || !reason.trim()}>
            {active ? "Run selected actions" : "Declare incident"}
          </button>
          <button type="button" onClick={close}>Cancel</button>
        </div>
      </form>}
    </div>
  </div>;
}
