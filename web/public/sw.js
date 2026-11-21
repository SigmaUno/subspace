"use strict";

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

function safeURL(value) {
  const fallback = new URL("/", self.location.origin).href;
  try {
    const url = new URL(value || "/", self.location.origin);
    return url.origin === self.location.origin || url.protocol === "https:" ? url.href : fallback;
  } catch {
    return fallback;
  }
}

self.addEventListener("push", (event) => {
  let message = {};
  try {
    message = event.data ? event.data.json() : {};
  } catch {
    message = {};
  }
  const limit = typeof Notification !== "undefined" && Notification.maxActions ? Notification.maxActions : 2;
  const actions = Array.isArray(message.actions) ? message.actions.slice(0, limit) : [];
  const links = { "": safeURL(message.url) };
  for (const action of actions) links[action.action] = safeURL(action.url);
  event.waitUntil(self.registration.showNotification(message.title || "Subspace", {
    body: message.body || "",
    tag: message.tag || undefined,
    renotify: Boolean(message.tag),
    timestamp: message.ts ? Date.parse(message.ts) : Date.now(),
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
    actions: actions.map((action) => ({ action: action.action, title: action.title })),
    data: { links },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const links = event.notification.data?.links ?? {};
  const target = links[event.action] || links[""] || safeURL("/");
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const same = windows.find((client) => client.url === target);
    if (same) return same.focus();
    return self.clients.openWindow(target);
  })());
});

self.addEventListener("pushsubscriptionchange", (event) => {
  const old = event.oldSubscription;
  if (!old?.options?.applicationServerKey) return;
  event.waitUntil((async () => {
    const subscription = event.newSubscription ??
      await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: old.options.applicationServerKey });
    await fetch("/api/v1/push/subscriptions", {
      method: "POST",
      credentials: "same-origin",
      cache: "no-store",
      headers: { "Content-Type": "application/json", "X-Subspace-Request": "1" },
      body: JSON.stringify(subscription.toJSON()),
    });
    await fetch("/api/v1/push/subscriptions", {
      method: "DELETE",
      credentials: "same-origin",
      cache: "no-store",
      headers: { "Content-Type": "application/json", "X-Subspace-Request": "1" },
      body: JSON.stringify({ endpoint: old.endpoint }),
    });
  })());
});
