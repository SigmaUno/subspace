"use client";

import { useEffect, useState } from "react";
import { api, ApiError, useResource, useSubmit } from "../../../../lib/client";
import type { PushDevice, PushDeviceList, PushKey } from "../../../../lib/push-types";
import { Feedback } from "../shared/components";
import { formatTime } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

type BrowserState = "checking" | "unsupported" | "denied" | "off" | "on";

function pushSupported() {
  return window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

function keyBytes(key: string) {
  const base64 = key.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(key.length / 4) * 4, "=");
  return Uint8Array.from(atob(base64), (char) => char.charCodeAt(0));
}

function sameKey(subscription: PushSubscription, key: Uint8Array) {
  const current = subscription.options.applicationServerKey;
  if (!current) return false;
  const bytes = new Uint8Array(current);
  return bytes.length === key.length && bytes.every((byte, index) => byte === key[index]);
}

async function browserSubscription() {
  const registration = await navigator.serviceWorker.getRegistration("/");
  return registration ? registration.pushManager.getSubscription() : null;
}

async function browserState(): Promise<BrowserState> {
  if (!pushSupported()) return "unsupported";
  if (Notification.permission === "denied") return "denied";
  return await browserSubscription() ? "on" : "off";
}

function deviceName() {
  const agent = navigator.userAgent;
  const platform = /Android/.test(agent) ? "Android" : /iPhone|iPad/.test(agent) ? "iOS" : /Mac/.test(agent) ? "macOS"
    : /Windows/.test(agent) ? "Windows" : /Linux/.test(agent) ? "Linux" : "";
  const browser = /Edg\//.test(agent) ? "Edge" : /Firefox\//.test(agent) ? "Firefox" : /Chrome\//.test(agent) ? "Chrome"
    : /Safari\//.test(agent) ? "Safari" : "Browser";
  return [browser, platform].filter(Boolean).join(" on ");
}

async function forget(body: { id?: string; endpoint?: string }) {
  try {
    await api<void>("push/subscriptions", "DELETE", body);
  } catch (error) {
    if (!(error instanceof ApiError && error.status === 404)) throw error;
  }
}

const stateLabels: Record<BrowserState, string> = {
  checking: "checking", unsupported: "not supported", denied: "blocked", off: "off", on: "on",
};

export function PushPanel() {
  const devices = useResource<PushDeviceList>("push/subscriptions", 0);
  const [state, setState] = useState<BrowserState>("checking");
  const toggle = useSubmit();
  const remove = useSubmit();

  useEffect(() => {
    let live = true;
    browserState()
      .then((next) => { if (live) setState(next); })
      .catch(() => { if (live) setState("off"); });
    return () => { live = false; };
  }, []);

  async function enable() {
    const permission = await Notification.requestPermission();
    if (permission !== "granted") {
      setState(permission === "denied" ? "denied" : "off");
      throw new Error("Notifications were not allowed for this site.");
    }
    const { public_key: publicKey } = await api<PushKey>("push/vapid-key");
    const key = keyBytes(publicKey);
    await navigator.serviceWorker.register("/sw.js", { scope: "/", updateViaCache: "none" });
    const registration = await navigator.serviceWorker.ready;
    let subscription = await registration.pushManager.getSubscription();
    if (subscription && !sameKey(subscription, key)) {
      await subscription.unsubscribe();
      subscription = null;
    }
    subscription ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    await api<PushDevice>("push/subscriptions", "POST", { ...subscription.toJSON(), name: deviceName() });
    setState("on");
    devices.refresh();
  }

  async function disable() {
    const subscription = await browserSubscription();
    if (subscription) {
      await forget({ endpoint: subscription.endpoint });
      await subscription.unsubscribe();
    }
    setState("off");
    devices.refresh();
  }

  const list = devices.data?.subscriptions ?? [];
  const blocked = state === "checking" || state === "unsupported" || state === "denied";
  return <section className="panel stack">
    <div className="heading"><h2>Push notifications</h2>
      <span className={`pill ${state === "on" ? "active" : ""}`}>{stateLabels[state]} on this device</span></div>
    {state === "unsupported" && <EmptyState>This browser cannot receive Web Push here. On iPhone and iPad, add Subspace to the home screen and open it from there.</EmptyState>}
    {state === "denied" && <EmptyState>Notifications are blocked for this site. Allow them in the browser settings, then reload.</EmptyState>}
    <form className="stack" onSubmit={(event) => toggle.submit(event, state === "on" ? disable : enable)}>
      <div className="actions">
        <button className="primary" disabled={toggle.busy || blocked}>{state === "on" ? "Turn off on this device" : "Turn on for this device"}</button>
      </div>
      <Feedback error={toggle.error} />
    </form>
    <ResourceState error={devices.error} loading={devices.data === undefined} retry={devices.refresh} rows={2} />
    {devices.data && !list.length && <EmptyState>No devices receive push notifications.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Device</th><th>Service</th><th>Subscribed</th><th>Last delivery</th><th>Actions</th></tr></thead>
        <tbody>{list.map((device) => (
          <tr key={device.id}>
            <th scope="row">{device.name || device.id}{device.failures > 0 && <span className="meta"> · {device.failures} failed</span>}</th>
            <td>{device.host}</td>
            <td>{formatTime(device.created_at)}</td>
            <td>{formatTime(device.last_success_at)}</td>
            <td>
              <form onSubmit={(event) => remove.submit(event, async () => {
                await forget({ id: device.id });
                devices.refresh();
              })}>
                <button className="text-button" disabled={remove.busy} aria-label={`Remove ${device.name || device.id}`}>Remove</button>
              </form>
            </td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    <Feedback error={remove.error} />
  </section>;
}
