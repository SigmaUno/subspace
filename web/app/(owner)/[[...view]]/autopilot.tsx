"use client";

import { useState } from "react";
import { api, ApiError, label, optionalResource, useResource } from "../../../lib/client";
import { Feedback } from "./components";

interface AutopilotState {
  state: string;
  last_tick: string | null;
  last_error?: string;
  last_actions: AutopilotAction[] | null;
  briefs: { id: string; title: string; enabled: boolean }[] | null;
}

interface AutopilotAction {
  kind: string;
  brief_id: string;
  task_id?: string;
  agent_id?: string;
  spawn?: { n?: number; worker_kind?: string; model?: string };
  at: string;
  error?: string;
  code?: string;
}

interface SpawnInfo {
  id: string;
  brief_id: string;
  agent_id: string;
  kind: string;
  model: string;
  status: string;
  spawner: string;
  error: string | null;
  requested_by: string;
  created_at: string;
}

const visibleActions = 8;
const visibleSpawns = 8;

function describe(action: AutopilotAction) {
  const target = [action.task_id, action.agent_id && `→ ${action.agent_id}`].filter(Boolean).join(" ");
  const spawn = action.spawn ? `${action.spawn.n ?? 1} × ${action.spawn.worker_kind ?? ""} ${action.spawn.model ?? ""}`.trim() : "";
  return [label(action.kind), action.brief_id, target || spawn].filter(Boolean).join(" · ");
}

function stamp(time: string) {
  return new Date(time).toLocaleTimeString([], { timeStyle: "medium" });
}

function Spawns({ revision }: { revision: number }) {
  const feed = useResource("spawns", revision, optionalResource<{ spawns: SpawnInfo[] | null }>);
  const [stopMissing, setStopMissing] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  if (feed.data === null) return null;
  const spawns = (feed.data?.spawns ?? []).slice().reverse().slice(0, visibleSpawns);

  async function stop(spawn: SpawnInfo) {
    setBusy(spawn.id);
    setError("");
    try {
      await api(`spawns/${encodeURIComponent(spawn.id)}/stop`, "POST", {});
      feed.refresh();
    } catch (cause) {
      if (cause instanceof ApiError && [404, 405, 501].includes(cause.status)) setStopMissing(true);
      else setError(cause instanceof Error ? cause.message : "Could not stop the spawn.");
    } finally {
      setBusy("");
    }
  }

  return <div className="stack">
    <h3>Spawns</h3>
    <Feedback error={error || feed.error} loading={feed.data === undefined && !feed.error} />
    {feed.data !== undefined && !spawns.length && <p className="empty">No spawns yet.</p>}
    {spawns.map((spawn) => <div className="attempt-row" key={spawn.id}>
      <span>{spawn.id}</span>
      <span><span className="pill" data-status={spawn.status === "failed" ? "blocked" : spawn.status === "spawned" ? "in_progress" : "assigned"}>{spawn.status}</span></span>
      <span className="meta">{spawn.agent_id} · {spawn.kind} · {spawn.spawner} · {spawn.brief_id}{spawn.error && ` · ${spawn.error}`}</span>
      {spawn.status === "spawned" && !stopMissing
        ? <button type="button" className="danger" data-write disabled={busy === spawn.id} onClick={() => stop(spawn)}>Stop</button>
        : <time className="meta" dateTime={spawn.created_at}>{stamp(spawn.created_at)}</time>}
    </div>)}
  </div>;
}

export function AutopilotPanel({ revision, spawnRevision }: { revision: number; spawnRevision: number }) {
  const feed = useResource("autopilot", revision, optionalResource<AutopilotState>);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  if (feed.data === null) return null;
  const state = feed.data;
  const actions = (state?.last_actions ?? []).slice().reverse().slice(0, visibleActions);

  async function toggle(id: string, enabled: boolean) {
    setBusy(id);
    setError("");
    try {
      await api(`autopilot/briefs/${encodeURIComponent(id)}`, "POST", { enabled });
      feed.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not update autopilot.");
    } finally {
      setBusy("");
    }
  }

  return <section className="panel stack">
    <div className="heading"><h2>Autopilot</h2>
      {state && <span className="pill" data-status={state.state === "running" ? "in_progress" : state.state === "paused" ? "changes_requested" : "backlog"}>{state.state}</span>}</div>
    <Feedback error={error || feed.error} loading={!state && !feed.error} />
    {state && <>
      <p className="meta">{state.last_tick ? `Last tick ${stamp(state.last_tick)}` : "No tick yet"}</p>
      {state.last_error && <p className="error" role="alert">{state.last_error}</p>}
      {state.briefs?.length
        ? <div className="stack">{state.briefs.map((brief) => <label key={brief.id} className="actions">
          <input type="checkbox" data-write checked={brief.enabled} disabled={busy === brief.id}
            onChange={(event) => toggle(brief.id, event.target.checked)} />
          <span>{brief.title}</span><span className="meta">{brief.id}</span></label>)}</div>
        : <p className="empty">No open briefs.</p>}
      <h3>Last actions</h3>
      {actions.length
        ? <ul className="stack" aria-label="Autopilot actions">
          {actions.map((action, index) => <li key={`${action.at}-${index}`}>
            <time className="meta" dateTime={action.at}>{stamp(action.at)}</time> {describe(action)}
            {action.error && <span className="stale"> · {action.error}</span>}</li>)}
        </ul>
        : <p className="empty">No actions yet.</p>}
      <Spawns revision={spawnRevision} />
    </>}
  </section>;
}
