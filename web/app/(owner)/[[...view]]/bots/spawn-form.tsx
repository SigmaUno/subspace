"use client";

import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../../lib/client";
import type { AgentProfile } from "../../../../lib/profiles-types";
import type { Brief } from "../../../../lib/types";
import { Feedback } from "../shared/components";
import { useToast } from "../shared/ui";

const spawnKinds = [
  ["claude-code", "Claude Code"], ["codex", "Codex"], ["opencode", "OpenCode"],
  ["opencode-anthropic", "OpenCode (Anthropic)"], ["opencode-deepseek", "OpenCode (DeepSeek)"],
];
const spawnRoles = ["worker", "tester"];

export function SpawnForm({ profiles, revision, complete }: { profiles: AgentProfile[]; revision: number; complete: () => void }) {
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=1000", revision);
  const [profile, setProfile] = useState("");
  const chosen = profiles.find((item) => item.name === profile);
  const kind = spawnKinds.some(([id]) => id === chosen?.kind) ? chosen?.kind : undefined;
  const role = spawnRoles.includes(chosen?.role ?? "") ? chosen?.role : undefined;
  const open = briefs.data?.briefs ?? [];
  const form = useSubmit();
  const toast = useToast();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const count = Number(value(fields, "n"));
    await api("bots/spawn", "POST", {
      brief_id: value(fields, "brief_id"), kind: value(fields, "kind"), model: value(fields, "model"),
      n: count, role: value(fields, "role"),
    });
    toast(`Requested ${count} ${count === 1 ? "spawn" : "spawns"}.`);
    complete();
  })}>
    <h2>Spawn agents</h2>
    <div className="form-grid">
      <label>Profile<select value={profile} onChange={(event) => setProfile(event.target.value)}>
        <option value="">None</option>
        {profiles.map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}
      </select></label>
      <label>Brief<select name="brief_id" required defaultValue="">
        <option value="" disabled>Select a brief</option>
        {open.map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
      </select></label>
      <label>Kind<select key={`kind:${profile}`} name="kind" defaultValue={kind ?? "claude-code"}>
        {spawnKinds.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Model<input key={`model:${profile}`} name="model" defaultValue={chosen?.model ?? ""} placeholder="claude-sonnet-5-5" required /></label>
      <label>Count<input key={`n:${profile}`} name="n" type="number" min="1" max="50" defaultValue={chosen?.count || 1} required /></label>
      <label>Role<select key={`role:${profile}`} name="role" defaultValue={role ?? "worker"}>
        {spawnRoles.map((item) => <option key={item} value={item}>{item}</option>)}
      </select></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Request spawn</button></div>
  </form>;
}
