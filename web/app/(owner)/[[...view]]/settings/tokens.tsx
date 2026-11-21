"use client";

import { useContext, useState } from "react";
import { api, useSubmit, value } from "../../../../lib/client";
import type { CreatedToken, OwnerToken, TokenList } from "../../../../lib/settings-types";
import { formatTime } from "../shared/format";
import { CopyBlock } from "../shared/copy";
import { Clock, Feedback } from "../shared/components";
import { EmptyState } from "../shared/states";
import { useAction, useConfirm, useToast } from "../shared/ui";

function tokenState(token: OwnerToken, now: number) {
  if (token.revoked_at) return "revoked";
  if (token.expires_at && Date.parse(token.expires_at) <= now) return "expired";
  return "active";
}

export function TokensPanel({ list, refresh }: { list: TokenList; refresh: () => void }) {
  const tokens: OwnerToken[] = list.tokens ?? [];
  const now = useContext(Clock);
  const [created, setCreated] = useState<CreatedToken>();
  const action = useAction();
  const confirm = useConfirm();
  const toast = useToast();
  const form = useSubmit();

  async function revoke(token: OwnerToken) {
    const accepted = await confirm({
      title: `Revoke token ${token.id}?`,
      message: `Clients using ${token.name} stop working immediately.`,
      confirm: "Revoke",
    });
    if (accepted && await action.run(token.id, () => api<OwnerToken>(`tokens/${encodeURIComponent(token.id)}/revoke`, "POST", {}), `Revoked token ${token.id}.`)) refresh();
  }

  return <section className="panel stack">
    <div className="heading"><h2>API tokens</h2></div>
    <form className="stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
      const expires = value(fields, "expires_at");
      const result = await api<CreatedToken>("tokens", "POST", {
        name: value(fields, "name"),
        scope: value(fields, "scope"),
        expires_at: expires ? new Date(expires).toISOString() : null,
      });
      setCreated(result);
      toast(`Created token ${result.token.id}.`);
      element.reset();
      refresh();
    })}>
      <fieldset className="form-grid" disabled={form.busy}>
        <label>Name<input name="name" required maxLength={80} /></label>
        <label>Scope<select name="scope" defaultValue="viewer">
          <option value="viewer">viewer (read only)</option>
          <option value="owner">owner (full access)</option>
        </select></label>
        <label>Expires<input name="expires_at" type="datetime-local" /></label>
      </fieldset>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy}>Create token</button></div>
    </form>
    {created && <div className="stack">
      <p className="notice">Save this secret now. It is displayed once for {created.token.id}.</p>
      <CopyBlock label="API token" text={created.secret} />
      <div className="actions"><button type="button" onClick={() => setCreated(undefined)}>Dismiss secret</button></div>
    </div>}
    {!tokens.length && <EmptyState>No owner API tokens.</EmptyState>}
    {!!tokens.length && <div className="table-wrap" role="region" aria-label="API tokens" tabIndex={0}>
      <table>
        <thead><tr><th>Name</th><th>Scope</th><th>State</th><th>Created</th><th>Expires</th><th>Last used</th><th><span className="meta">Action</span></th></tr></thead>
        <tbody>{tokens.map((token) => {
          const state = tokenState(token, now);
          return <tr key={token.id}>
            <th scope="row">{token.name} <span className="meta">{token.id}</span></th>
            <td>{token.scope}</td>
            <td><span className={`pill ${state === "active" ? "active" : "revoked"}`}>{state}</span></td>
            <td>{formatTime(token.created_at)}</td>
            <td>{token.expires_at ? formatTime(token.expires_at) : "never"}</td>
            <td>{formatTime(token.last_used_at)}</td>
            <td>{!token.revoked_at && <button type="button" className="danger" data-write disabled={action.busy === token.id} onClick={() => revoke(token)}
              aria-label={`Revoke token ${token.name}`}>Revoke</button>}</td>
          </tr>;
        })}</tbody>
      </table>
    </div>}
  </section>;
}
