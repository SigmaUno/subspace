"use client";

import { useState } from "react";
import { api, useSubmit, value } from "../../../lib/client";
import type { AgentProfile } from "../../../lib/profiles-types";
import { Feedback } from "./components";
import { EmptyState } from "./states";
import { useAction, useConfirm, useToast } from "./ui";
import { agentKinds, agentRoles } from "../../../lib/types";

function profilePath(name: string) {
  return `agent-profiles/${encodeURIComponent(name)}`;
}

function ProfileForm({ profile, done, cancel }: { profile?: AgentProfile; done: () => void; cancel: () => void }) {
  const form = useSubmit();
  const toast = useToast();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const body = {
      name: profile?.name ?? value(fields, "name"), role: value(fields, "role"), kind: value(fields, "kind"),
      model: value(fields, "model"), provider: value(fields, "provider"), expires_in: value(fields, "expires_in"),
      count: Number(value(fields, "count") || 0), callback_url: value(fields, "callback_url"),
      description: value(fields, "description"),
    };
    if (profile) await api<AgentProfile>(profilePath(profile.name), "PUT", body);
    else await api<AgentProfile>("agent-profiles", "POST", body);
    toast(`Saved profile ${body.name}.`);
    done();
  })}>
    <h3>{profile ? `Edit ${profile.name}` : "New profile"}</h3>
    <div className="form-grid">
      <label>Name<input name="name" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={63} required
        defaultValue={profile?.name} readOnly={!!profile} /></label>
      <label>Role<select name="role" defaultValue={profile?.role ?? "worker"}>
        {agentRoles.map((role) => <option key={role} value={role}>{role}</option>)}
      </select></label>
      <label>Kind<select name="kind" defaultValue={profile?.kind ?? "claude-code"}>
        {agentKinds.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Model<input name="model" placeholder="claude-sonnet-5-5" defaultValue={profile?.model} /></label>
      <label>Provider<input name="provider" placeholder="Derived from kind" defaultValue={profile?.provider} /></label>
      <label>Token lifetime<input name="expires_in" placeholder="720h, empty = never" defaultValue={profile?.expires_in} /></label>
      <label>Default bulk count<input name="count" type="number" min="0" max="1000" defaultValue={profile?.count ?? 0} /></label>
      <label>Callback URL<input name="callback_url" type="url" maxLength={2048} defaultValue={profile?.callback_url} /></label>
    </div>
    <label>Description<input name="description" maxLength={500} defaultValue={profile?.description} /></label>
    <Feedback error={form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>{profile ? "Save profile" : "Create profile"}</button>
      <button type="button" onClick={cancel}>Cancel</button>
    </div>
  </form>;
}

export function ProfilesPanel({ profiles, refresh, use }: {
  profiles: AgentProfile[];
  refresh: () => void;
  use: (profile: AgentProfile) => void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const action = useAction();
  const confirm = useConfirm();
  const current = profiles.find((profile) => profile.name === editing);

  async function remove(profile: AgentProfile) {
    const accepted = await confirm({
      title: `Delete profile ${profile.name}?`,
      message: "Bots already created from it keep working.",
      confirm: "Delete",
    });
    if (accepted && await action.run(profile.name, () => api<void>(profilePath(profile.name), "DELETE"), `Deleted profile ${profile.name}.`)) refresh();
  }

  function finish() {
    setEditing(null);
    refresh();
  }

  return <section className="stack" aria-labelledby="profiles-heading">
    <div className="heading"><h2 id="profiles-heading">Profiles</h2>
      <button data-write onClick={() => setEditing(editing === "" ? null : "")}>{editing === "" ? "Close form" : "New profile"}</button></div>
    {editing === "" && <ProfileForm done={finish} cancel={() => setEditing(null)} />}
    {current && <ProfileForm key={current.name} profile={current} done={finish} cancel={() => setEditing(null)} />}
    {!profiles.length && <EmptyState>No profiles.</EmptyState>}
    {!!profiles.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Profile</th><th>Role</th><th>Kind</th><th>Model / provider</th><th>Defaults</th><th>Actions</th></tr></thead>
        <tbody>{profiles.map((profile) => (
          <tr key={profile.name}>
            <th scope="row">{profile.name}<div className="meta">{profile.description}</div></th>
            <td>{profile.role}</td>
            <td>{profile.kind}</td>
            <td>{profile.model || "—"}<div className="meta">{profile.provider || "from kind"}</div></td>
            <td>{profile.count ? `${profile.count} bots` : "—"}
              <div className="meta">{profile.expires_in ? `token ${profile.expires_in}` : "token never expires"}</div></td>
            <td><div className="actions" data-write>
              <button onClick={() => use(profile)}>Use for bulk add</button>
              <button onClick={() => setEditing(profile.name)}>Edit</button>
              <button className="danger" disabled={action.busy === profile.name} onClick={() => remove(profile)}>Delete</button>
            </div></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
