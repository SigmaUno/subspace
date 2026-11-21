"use client";

import { useState } from "react";
import { ApiError, api, useResource, useSubmit, value } from "../../../../lib/client";
import type { Bot, BotAction, BotList, BotRegistration } from "../../../../lib/bots-types";
import type { AgentProfile, ProfileList } from "../../../../lib/profiles-types";
import type { Thread } from "../../../../lib/types";
import { agentKinds, agentRoles } from "../../../../lib/types";
import { Feedback, Heartbeat } from "../shared/components";
import { CopyBlock } from "../shared/copy";
import { ProfilesPanel } from "./profiles";
import { SpawnsPanel } from "./spawns";
import { EmptyState, PageHeading, ResourceState } from "../shared/states";
import { useConfirm, useToast } from "../shared/ui";

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

type FormStart = { bulk: boolean; profile: string };

function AddBotForm({ complete, profiles, start }: { complete: (items: BotRegistration[]) => void; profiles: AgentProfile[]; start: FormStart }) {
  const [bulk, setBulk] = useState(start.bulk);
  const [profile, setProfile] = useState(start.profile);
  const chosen = profiles.find((item) => item.name === profile);
  const fromProfile = (text?: string) => chosen && <option value="">{text || "—"} (profile)</option>;
  const form = useSubmit();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const common = {
      role: value(fields, "role"), kind: value(fields, "kind"),
      model: value(fields, "model"), provider: value(fields, "provider"),
      ...(chosen ? { profile: chosen.name } : {}),
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
      <label>Profile<select value={profile} onChange={(event) => setProfile(event.target.value)}>
        <option value="">None</option>
        {profiles.map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}
      </select></label>
      <label>{bulk ? "Name prefix" : "Name"}
        <input name="id" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={bulk ? 58 : 63} required />
      </label>
      {bulk && <label>Count<input key={profile} name="count" type="number" min="1" max="1000" defaultValue={chosen?.count || 2} required /></label>}
      <label>Role<select key={`role:${profile}`} name="role" defaultValue={chosen ? "" : "worker"}>
        {fromProfile(chosen?.role)}
        {agentRoles.map((role) => <option key={role} value={role}>{role}</option>)}
      </select></label>
      <label>Kind<select key={`kind:${profile}`} name="kind" defaultValue={chosen ? "" : "claude-code"}>
        {fromProfile(chosen?.kind)}
        {agentKinds.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Model<input name="model" placeholder={chosen?.model || "claude-sonnet-5-5"} required={!chosen} /></label>
      <label>Provider override<input name="provider" placeholder={chosen?.provider || "Derived from kind"} /></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>{bulk ? "Add bots" : "Add bot"}</button></div>
  </form>;
}

const completed: Record<BotAction, string> = {
  pause: "Paused", resume: "Resumed", revoke: "Revoked", rotate: "Rotated the token of", spawn: "Spawn requested for",
};

const endpoints: Record<BotAction, (id: string) => string> = {
  pause: (id) => `bots/${id}/pause`,
  resume: (id) => `agents/${id}/resume`,
  revoke: (id) => `agents/${id}/revoke`,
  rotate: (id) => `agents/${id}/rotate-token`,
  spawn: (id) => `bots/${id}/spawn`,
};

function BotActions({ bot, busy, run }: { bot: Bot; busy: boolean; run: (bot: Bot, action: BotAction) => void }) {
  const live = bot.status !== "revoked";
  return <div className="actions" data-write>
    {bot.status === "active" && <button disabled={busy} onClick={() => run(bot, "pause")}>Pause</button>}
    {bot.status === "paused" && <button disabled={busy} onClick={() => run(bot, "resume")}>Resume</button>}
    {bot.status === "active" && <button disabled={busy} onClick={() => run(bot, "spawn")}>Spawn</button>}
    {live && <button disabled={busy} onClick={() => run(bot, "rotate")}>Rotate token</button>}
    {live && <button className="danger" disabled={busy} onClick={() => run(bot, "revoke")}>Revoke</button>}
  </div>;
}

export function BotsView({ revision, openThread }: { revision: number; openThread: (thread: Thread) => void }) {
  const bots = useResource<BotList>("agents?limit=1000", revision);
  const profiles = useResource<ProfileList>("agent-profiles", revision);
  const [showForm, setShowForm] = useState(false);
  const [start, setStart] = useState<FormStart>({ bulk: false, profile: "" });
  const [formKey, setFormKey] = useState(0);
  const [credentials, setCredentials] = useState<BotRegistration[] | null>(null);
  const [busy, setBusy] = useState("");
  const confirm = useConfirm();
  const toast = useToast();

  async function run(bot: Bot, action: BotAction) {
    if (action === "revoke" && !await confirm({
      title: `Revoke ${bot.id}?`, message: "Its token stops working immediately.", confirm: "Revoke",
    })) return;
    if (action === "rotate" && !await confirm({
      title: `Rotate the token for ${bot.id}?`, message: "The old token stops working.", confirm: "Rotate",
    })) return;
    setBusy(bot.id);
    try {
      const path = endpoints[action](encodeURIComponent(bot.id));
      const result = await api<Partial<BotRegistration>>(path, "POST", action === "revoke" ? { reason: "" } : {});
      if (action === "rotate" && result.agent && result.connect) setCredentials([{ agent: result.agent, connect: result.connect }]);
      toast(`${completed[action]} ${bot.id}.`);
      bots.refresh();
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404 && endpoints[action](bot.id).startsWith("bots/")) {
        toast(`The ${action} action is not available on this server.`, "error");
      } else {
        toast(cause instanceof Error ? cause.message : "Request failed.", "error");
      }
    } finally {
      setBusy("");
    }
  }

  const items = bots.data?.agents ?? [];
  const presets = profiles.data?.profiles ?? [];

  function openForm(next: FormStart) {
    setStart(next);
    setFormKey((key) => key + 1);
    setShowForm(true);
  }

  return <>
    <PageHeading title="Bots">
      <button className="primary" data-write disabled={!!credentials} onClick={() => showForm ? setShowForm(false) : openForm({ bulk: false, profile: "" })}>{showForm ? "Close form" : "Add bot"}</button></PageHeading>
    {showForm && <AddBotForm key={formKey} profiles={presets} start={start}
      complete={(added) => { setCredentials(added); setShowForm(false); bots.refresh(); }} />}
    {credentials && <Credentials items={credentials} dismiss={() => setCredentials(null)} />}
    <ResourceState error={bots.error} loading={!bots.data} retry={bots.refresh} rows={5} />
    {bots.data && !items.length && <EmptyState>No bots registered.</EmptyState>}
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
    <ResourceState error={profiles.error} loading={!profiles.data} retry={profiles.refresh} rows={3} />
    {profiles.data && <ProfilesPanel profiles={presets} refresh={profiles.refresh} use={(profile) => openForm({ bulk: true, profile: profile.name })} />}
    <SpawnsPanel revision={revision} />
  </>;
}
