"use client";

import { useState } from "react";
import { ApiError, api, useResource, useSubmit, value } from "../../../lib/client";
import type { Bot, BotAction, BotList, BotRegistration } from "../../../lib/bots-types";
import type { Thread } from "../../../lib/types";
import { Feedback, Heartbeat } from "./components";

const roles = ["leader", "worker", "tester", "reviewer"];
const kinds = [
  ["claude-code", "Claude Code"], ["codex", "Codex"], ["opencode", "OpenCode"], ["other", "Other"],
];

function CopyBlock({ label, text }: { label: string; text: string }) {
  const [feedback, setFeedback] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setFeedback("Copied");
    } catch {
      setFeedback("Select and copy the text.");
    }
  }
  return <div className="secret">
    <div className="heading"><strong>{label}</strong>
      <button type="button" onClick={copy} aria-label={`Copy ${label}`}>Copy</button>
    </div>
    <pre>{text}</pre>
    {feedback && <span className="meta" role="status">{feedback}</span>}
  </div>;
}

function Credentials({ items, dismiss }: { items: BotRegistration[]; dismiss: () => void }) {
  return <section className="panel stack">
    <div className="heading"><h2>Connect snippets</h2><button onClick={dismiss}>Dismiss credentials</button></div>
    <p className="notice">Save these now. Tokens are displayed once.</p>
    {items.map(({ agent, connect }, index) => (
      <details key={agent.id} open={index === 0}>
        <summary>{agent.id}</summary>
        <CopyBlock label="Agent token" text={connect.token} />
        <CopyBlock label="Join line" text={connect.join_line} />
        <CopyBlock label="Claude Code" text={connect.snippets.claude_code} />
        <CopyBlock label="Codex config.toml" text={connect.snippets.codex_toml} />
        <CopyBlock label="OpenCode opencode.json" text={connect.snippets.opencode_json} />
      </details>
    ))}
  </section>;
}

function AddBotForm({ complete }: { complete: (items: BotRegistration[]) => void }) {
  const [bulk, setBulk] = useState(false);
  const form = useSubmit();
  return <form className="panel stack" onSubmit={(event) => form.submit(event, async (fields) => {
    const common = {
      role: value(fields, "role"), kind: value(fields, "kind"),
      model: value(fields, "model"), provider: value(fields, "provider"),
    };
    const id = value(fields, "id");
    if (bulk) {
      const result = await api<{ agents: BotRegistration[] }>("agents/bulk", "POST", {
        ...common, prefix: id, count: Number(value(fields, "count")),
      });
      complete(result.agents);
    } else {
      complete([await api<BotRegistration>("agents", "POST", { ...common, id })]);
    }
  })}>
    <h2>{bulk ? "Bulk add bots" : "Add bot"}</h2>
    <label className="inline"><input type="checkbox" checked={bulk} onChange={(event) => setBulk(event.target.checked)} /> Bulk add</label>
    <div className="form-grid">
      <label>{bulk ? "Name prefix" : "Name"}
        <input name="id" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={bulk ? 58 : 63} required />
      </label>
      {bulk && <label>Count<input name="count" type="number" min="1" max="1000" defaultValue="2" required /></label>}
      <label>Role<select name="role" defaultValue="worker">
        {roles.map((role) => <option key={role} value={role}>{role}</option>)}
      </select></label>
      <label>Kind<select name="kind" defaultValue="claude-code">
        {kinds.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Model<input name="model" placeholder="claude-sonnet-5-5" required /></label>
      <label>Provider override<input name="provider" placeholder="Derived from kind" /></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>{bulk ? "Add bots" : "Add bot"}</button></div>
  </form>;
}

const endpoints: Record<BotAction, (id: string) => string> = {
  pause: (id) => `bots/${id}/pause`,
  resume: (id) => `agents/${id}/resume`,
  revoke: (id) => `agents/${id}/revoke`,
  rotate: (id) => `agents/${id}/rotate-token`,
  spawn: (id) => `bots/${id}/spawn`,
};

function BotActions({ bot, busy, run }: { bot: Bot; busy: boolean; run: (bot: Bot, action: BotAction) => void }) {
  const live = bot.status !== "revoked";
  return <div className="actions">
    {bot.status === "active" && <button disabled={busy} onClick={() => run(bot, "pause")}>Pause</button>}
    {bot.status === "paused" && <button disabled={busy} onClick={() => run(bot, "resume")}>Resume</button>}
    {bot.status === "active" && <button disabled={busy} onClick={() => run(bot, "spawn")}>Spawn</button>}
    {live && <button disabled={busy} onClick={() => run(bot, "rotate")}>Rotate token</button>}
    {live && <button className="danger" disabled={busy} onClick={() => run(bot, "revoke")}>Revoke</button>}
  </div>;
}

export function BotsView({ revision, openThread }: { revision: number; openThread: (thread: Thread) => void }) {
  const bots = useResource<BotList>("agents?limit=1000", revision);
  const [showForm, setShowForm] = useState(false);
  const [credentials, setCredentials] = useState<BotRegistration[] | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function run(bot: Bot, action: BotAction) {
    if (action === "revoke" && !window.confirm(`Revoke ${bot.id}? Its token stops working immediately.`)) return;
    if (action === "rotate" && !window.confirm(`Rotate the token for ${bot.id}? The old token stops working.`)) return;
    setBusy(bot.id);
    setError("");
    setNotice("");
    try {
      const path = endpoints[action](encodeURIComponent(bot.id));
      const result = await api<Partial<BotRegistration>>(path, "POST", action === "revoke" ? { reason: "" } : {});
      if (action === "rotate" && result.agent && result.connect) setCredentials([{ agent: result.agent, connect: result.connect }]);
      if (action === "spawn") setNotice(`Spawn requested for ${bot.id}.`);
      bots.refresh();
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404 && endpoints[action](bot.id).startsWith("bots/")) {
        setError(`The ${action} action is not available on this server.`);
      } else {
        setError(cause instanceof Error ? cause.message : "Request failed.");
      }
    } finally {
      setBusy("");
    }
  }

  const items = bots.data?.agents ?? [];
  return <>
    <div className="heading"><h1>Bots</h1>
      <button className="primary" disabled={!!credentials} onClick={() => setShowForm(!showForm)}>{showForm ? "Close form" : "Add bot"}</button></div>
    {showForm && <AddBotForm complete={(added) => { setCredentials(added); setShowForm(false); bots.refresh(); }} />}
    {credentials && <Credentials items={credentials} dismiss={() => setCredentials(null)} />}
    <Feedback error={error || bots.error} loading={!bots.data} />
    {notice && <p className="meta" role="status">{notice}</p>}
    {bots.data && !items.length && <p className="empty">No bots registered.</p>}
    {!!items.length && <div className="table-wrap">
      <table>
        <thead><tr>
          <th>Bot</th><th>Role</th><th>Kind</th><th>Model / provider</th><th>Status</th>
          <th>Current task</th><th>Load</th><th>Last heartbeat</th><th>Actions</th>
        </tr></thead>
        <tbody>{items.map((bot) => (
          <tr key={bot.id}>
            <th scope="row"><button className="text-button" onClick={() => openThread({ kind: "agent", id: bot.id })}>{bot.id}</button></th>
            <td>{bot.role}</td>
            <td>{bot.kind}</td>
            <td>{bot.model}<div className="meta">{bot.provider}</div></td>
            <td><span className={`pill ${bot.status}`}>{bot.status}</span>{bot.pause_reason && <div className="meta">{bot.pause_reason}</div>}</td>
            <td>{bot.current_task_id
              ? <button className="text-button" onClick={() => openThread({ kind: "task", id: bot.current_task_id! })}>{bot.current_task_id}</button>
              : "—"}</td>
            <td>{bot.assigned_count} assigned{bot.capacity_weight ? <div className="meta">weight {bot.capacity_weight}</div> : null}</td>
            <td><Heartbeat time={bot.last_seen_at} watch={bot.status === "active"} /></td>
            <td><BotActions bot={bot} busy={busy === bot.id} run={run} /></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </>;
}
