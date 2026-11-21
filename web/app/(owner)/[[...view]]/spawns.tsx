"use client";

import { useContext, useState } from "react";
import { api, label, optionalResource, useResource } from "../../../lib/client";
import type { Spawn, SpawnList } from "../../../lib/spawns-types";
import { Clock } from "./components";
import { EmptyState, ResourceState } from "./states";
import { useAction, useConfirm } from "./ui";

const connectTimeout = 5 * 60_000;
const idleTTL = 15 * 60_000;

const tones = {
  pending: "backlog", connecting: "changes_requested", active: "in_progress",
  idle: "assigned", stopped: "cancelled", failed: "blocked",
} as const;

function span(ms: number) {
  const minutes = Math.max(0, Math.floor(ms / 60_000));
  if (minutes < 1) return "<1m";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h ${minutes % 60}m` : `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

function age(spawn: Spawn, now: number) {
  const end = spawn.stopped_at ? Date.parse(spawn.stopped_at) : now;
  return span(end - Date.parse(spawn.created_at));
}

function remaining(name: string, since: string, limit: number, now: number) {
  const left = Date.parse(since) + limit - now;
  return left > 0 ? `${name} in ${span(left)}` : `${name} due`;
}

function countdown(spawn: Spawn, now: number) {
  if (spawn.lifecycle === "connecting") return remaining("Connect timeout", spawn.created_at, connectTimeout, now);
  if (spawn.lifecycle === "idle" && spawn.idle_since) return remaining("Idle stop", spawn.idle_since, idleTTL, now);
}

export function SpawnsPanel({ revision }: { revision: number }) {
  const feed = useResource("spawns", revision, optionalResource<SpawnList>);
  const now = useContext(Clock);
  const [finished, setFinished] = useState(false);
  const action = useAction();
  const confirm = useConfirm();
  if (feed.data === null) return null;

  const all = (feed.data?.spawns ?? []).slice().sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  const running = all.filter((spawn) => spawn.lifecycle !== "stopped" && spawn.lifecycle !== "failed");
  const spawns = finished ? all : running;

  async function stop(spawn: Spawn) {
    const accepted = await confirm({
      title: `Stop ${spawn.id}?`,
      message: `${spawn.agent_id} is revoked and its process is asked to stop.`,
      confirm: "Stop",
    });
    if (accepted && await action.run(spawn.id, () => api(`spawns/${encodeURIComponent(spawn.id)}/stop`, "POST", {}), `Stopped ${spawn.id}.`)) feed.refresh();
  }

  return <section className="stack" aria-label="Spawns">
    <div className="heading"><h2>Spawns</h2>
      <label className="inline">
        <input type="checkbox" checked={finished} onChange={(event) => setFinished(event.target.checked)} /> Show stopped and failed ({all.length - running.length})
      </label>
    </div>
    <ResourceState error={feed.error} loading={!feed.data} retry={feed.refresh} rows={3} />
    {feed.data && !spawns.length && <EmptyState>{all.length ? "No running spawns." : "No spawns yet."}</EmptyState>}
    {!!spawns.length && <div className="table-wrap">
      <table>
        <thead><tr>
          <th>Spawn</th><th>Agent</th><th>Lifecycle</th><th>Process</th><th>Age</th><th>Timers</th><th>Actions</th>
        </tr></thead>
        <tbody>{spawns.map((spawn) => {
          const timer = now ? countdown(spawn, now) : undefined;
          return <tr key={spawn.id}>
            <th scope="row">{spawn.id}<div className="meta">{spawn.brief_id} · {spawn.spawner}</div></th>
            <td>{spawn.agent_id}<div className="meta">{spawn.kind} · {spawn.model}</div></td>
            <td><span className="pill" data-status={tones[spawn.lifecycle] ?? "backlog"}>{spawn.lifecycle}</span>
              {spawn.stop_reason && <div className="meta">{label(spawn.stop_reason)}</div>}
              {spawn.error && <div className="stale">{spawn.error}</div>}</td>
            <td>{spawn.process}</td>
            <td><time dateTime={spawn.created_at} title={spawn.created_at}>{now ? age(spawn, now) : "—"}</time></td>
            <td>{timer ?? "—"}</td>
            <td>{spawn.status === "spawned"
              ? <button type="button" className="danger" data-write disabled={action.busy === spawn.id} onClick={() => stop(spawn)}>Stop</button>
              : "—"}</td>
          </tr>;
        })}</tbody>
      </table>
    </div>}
  </section>;
}
