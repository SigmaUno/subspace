"use client";

import { useState } from "react";
import { api, commaList, safeURL, useResource, useSubmit, value } from "../../../lib/client";
import type { CreatedWebhook, DeliveryList, WebhookDelivery, WebhookList, WebhookOut } from "../../../lib/settings-types";
import type { Brief } from "../../../lib/types";
import { formatTime } from "./format";
import { CopyBlock } from "./copy";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";
import { useAction, useConfirm, useToast } from "./ui";

const deliveryClass = { pending: "paused", delivered: "active", dead: "revoked" } as const;

function Deliveries({ webhook, onChange }: { webhook: WebhookOut; onChange: () => void }) {
  const [state, setState] = useState("");
  const action = useAction();
  const path = `webhooks-out/${encodeURIComponent(webhook.id)}/deliveries?limit=50${state ? `&state=${state}` : ""}`;
  const list = useResource<DeliveryList>(path, 0);
  const deliveries = list.data?.deliveries ?? [];

  async function redeliver(delivery: WebhookDelivery) {
    const queued = await action.run(delivery.id, () => api<WebhookDelivery>(
      `webhooks-out/${encodeURIComponent(webhook.id)}/deliveries/${encodeURIComponent(delivery.id)}/redeliver`, "POST", {}),
    `Queued ${delivery.id} for redelivery.`);
    if (queued) {
      list.refresh();
      onChange();
    }
  }

  return <div className="stack">
    <div className="heading"><h3>Deliveries of {webhook.id}</h3>
      <label>State<select value={state} onChange={(event) => setState(event.target.value)}>
        <option value="">All</option>
        <option value="pending">Pending</option>
        <option value="delivered">Delivered</option>
        <option value="dead">Dead</option>
      </select></label></div>
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={2} />
    {list.data && !deliveries.length && <EmptyState>No deliveries.</EmptyState>}
    {!!deliveries.length && <div className="table-wrap" role="region" aria-label="Webhook deliveries" tabIndex={0}>
      <table>
        <thead><tr><th>Delivery</th><th>Event</th><th>State</th><th>Attempts</th><th>Last result</th><th>Next attempt</th><th><span className="meta">Action</span></th></tr></thead>
        <tbody>{deliveries.map((delivery) => <tr key={delivery.id}>
          <th scope="row">{delivery.id}</th>
          <td>{delivery.event}</td>
          <td><span className={`pill ${deliveryClass[delivery.state]}`}>{delivery.state}</span></td>
          <td>{delivery.attempts}</td>
          <td>{delivery.last_status ? `HTTP ${delivery.last_status}` : "—"}{delivery.last_error && <span className="meta"> · {delivery.last_error}</span>}</td>
          <td>{delivery.state === "pending" ? formatTime(delivery.next_attempt_at) : "—"}</td>
          <td>{delivery.state === "dead" && <button type="button" data-write disabled={action.busy === delivery.id}
            onClick={() => redeliver(delivery)} aria-label={`Redeliver ${delivery.id}`}>Redeliver</button>}</td>
        </tr>)}</tbody>
      </table>
    </div>}
  </div>;
}

export function WebhooksPanel({ list, refresh }: { list: WebhookList; refresh: () => void }) {
  const webhooks = list.webhooks ?? [];
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=1000", 0);
  const [created, setCreated] = useState<CreatedWebhook>();
  const [selected, setSelected] = useState("");
  const action = useAction();
  const confirm = useConfirm();
  const toast = useToast();
  const form = useSubmit();
  const active = webhooks.find((webhook) => webhook.id === selected);

  async function remove(webhook: WebhookOut) {
    const accepted = await confirm({
      title: `Delete webhook ${webhook.id}?`,
      message: "The webhook and its deliveries are removed.",
      confirm: "Delete",
    });
    if (!accepted) return;
    const removed = await action.run(webhook.id, () => api<void>(`webhooks-out/${encodeURIComponent(webhook.id)}`, "DELETE"), `Deleted webhook ${webhook.id}.`);
    if (!removed) return;
    if (selected === webhook.id) setSelected("");
    refresh();
  }

  return <>
    <section className="panel stack">
      <div className="heading"><h2>Outbound webhooks</h2></div>
      <form className="stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
        const events = commaList(value(fields, "events"));
        const result = await api<CreatedWebhook>("webhooks-out", "POST", { url: value(fields, "url"), events, brief_id: value(fields, "brief_id") });
        setCreated(result);
        toast(`Created webhook ${result.webhook.id}.`);
        setSelected(result.webhook.id);
        element.reset();
        refresh();
      })}>
        <fieldset className="form-grid" disabled={form.busy}>
          <label>Receiver URL<input name="url" type="url" required placeholder="https://hooks.example.com/subspace" /></label>
          <label>Events (comma separated)<input name="events" required placeholder="task.updated, decision.*" /></label>
          <label>Only brief<select name="brief_id" defaultValue="">
            <option value="">Every brief</option>
            {(briefs.data?.briefs ?? []).map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
          </select></label>
        </fieldset>
        <Feedback error={form.error} />
        <div className="actions"><button className="primary" disabled={form.busy}>Create webhook</button></div>
      </form>
      {created && <div className="stack">
        <p className="notice">Save this signing secret now. It is displayed once for {created.webhook.id}.</p>
        <CopyBlock label="Signing secret" text={created.secret} />
        <div className="actions"><button type="button" onClick={() => setCreated(undefined)}>Dismiss secret</button></div>
      </div>}
      {!webhooks.length && <EmptyState>No outbound webhooks.</EmptyState>}
      {!!webhooks.length && <div className="table-wrap" role="region" aria-label="Webhooks" tabIndex={0}>
        <table>
          <thead><tr><th>Webhook</th><th>Events</th><th>Brief</th><th>Pending</th><th>Delivered</th><th>Dead</th><th><span className="meta">Actions</span></th></tr></thead>
          <tbody>{webhooks.map((webhook) => {
            const url = safeURL(webhook.url);
            return <tr key={webhook.id}>
              <th scope="row">{webhook.id} <span className="meta">{url ? new URL(url).host : webhook.url}</span></th>
              <td>{webhook.events.join(", ")}</td>
              <td>{webhook.brief_id ?? "all"}</td>
              <td>{webhook.pending}</td>
              <td>{webhook.delivered}</td>
              <td>{webhook.dead ? <span className="pill revoked">{webhook.dead}</span> : 0}</td>
              <td><div className="actions">
                <button type="button" aria-pressed={webhook.id === selected} onClick={() => setSelected(webhook.id === selected ? "" : webhook.id)}
                  aria-label={`Deliveries of ${webhook.id}`}>Deliveries</button>
                <button type="button" className="danger" data-write disabled={action.busy === webhook.id} onClick={() => remove(webhook)} aria-label={`Delete ${webhook.id}`}>Delete</button>
              </div></td>
            </tr>;
          })}</tbody>
        </table>
      </div>}
    </section>
    {active && <section className="panel stack"><Deliveries key={active.id} webhook={active} onChange={refresh} /></section>}
  </>;
}
