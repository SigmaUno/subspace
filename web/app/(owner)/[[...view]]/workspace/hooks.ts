"use client";

import { useEffect, useRef, useState } from "react";
import { api, useResource } from "../../../../lib/client";
import type { Control } from "../../../../lib/types";

export function useTopbarHeight() {
  const topbar = useRef<HTMLElement>(null);
  const [height, setHeight] = useState(64);
  useEffect(() => {
    const element = topbar.current;
    if (!element) return;
    const observer = new ResizeObserver(() => setHeight(element.offsetHeight));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return { topbar, height };
}

export function useNow() {
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 15_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);
  return now;
}

export function useOwnerControls(state: ReturnType<typeof useResource<Control>>) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  async function togglePause() {
    if (!state.data) return;
    setPending(true);
    setError("");
    try {
      const action = state.data.paused ? "resume-all" : "pause-all";
      const result = await api<{ paused: boolean }>(action, "POST", {});
      state.setData({ ...state.data, paused: result.paused, pause_reason: null });
      state.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Control request failed.");
    } finally {
      setPending(false);
    }
  }

  async function signOut() {
    try {
      const response = await fetch("/session", { method: "DELETE", headers: { "X-Subspace-Request": "1" } });
      if (!response.ok) throw new Error("Could not sign out.");
      window.location.replace("/login");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not sign out.");
    }
  }

  return { pending, error, togglePause, signOut };
}
