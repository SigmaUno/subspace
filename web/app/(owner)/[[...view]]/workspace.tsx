"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { api, optionalResource, useResource } from "../../../lib/client";
import type { Control, Thread } from "../../../lib/types";
import { BotsView } from "./bots";
import { BriefsView } from "./board";
import { CharterView } from "./charter";
import { AgentsView, CapacityView, PlansView, QuestionsView } from "./operations";
import { Leaderboard } from "./leaderboard";
import { ThreadPanel } from "./thread";
import { TrendsView } from "./trends";
import { UsageView } from "./usage";
import { CostsView } from "./costs";
import { useLive } from "./live";
import { SettingsView } from "./settings";
import { OutputTile } from "./terminal";

import { DecisionInbox, type DecisionFeed } from "./decisions";
import { FilesView } from "./files";
import { Onboarding } from "./onboarding";
import { SwarmOverview } from "./overview";
import { CommandPalette } from "./search";
import { KeyboardShortcuts } from "./shortcuts";
import { Clock, Feedback } from "./components";
import { ConnectionStatus, ResourceState } from "./states";

export function Workspace({ section, briefID, architecture }: { section: string; briefID?: string; architecture: boolean }) {
  const topbar = useRef<HTMLElement>(null);
  const [topbarHeight, setTopbarHeight] = useState(64);
  useEffect(() => {
    const element = topbar.current;
    if (!element) return;
    const observer = new ResizeObserver(() => setTopbarHeight(element.offsetHeight));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 15_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);
  const [thread, setThread] = useState<Thread | null>(null);
  const [inboxOpen, setInboxOpen] = useState(section === "swarm");
  const [moreOpen, setMoreOpen] = useState(false);
  const { revisions, recent, status } = useLive("all");
  const state = useResource<Control>("state", revisions.control);
  const decisions = useResource("decisions", revisions.decisions, optionalResource<DecisionFeed>);
  const decisionCount = decisions.data?.count ?? decisions.data?.decisions?.length ??
    ((state.data?.counts?.open_questions ?? 0) + (state.data?.counts?.proposed_plans ?? 0));
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

  const nav = [
    { id: "swarm", name: "Swarm", href: "/" },
    { id: "briefs", name: "Briefs", href: "/briefs" },
    { id: "agents", name: "Agents", href: "/agents" },
    { id: "bots", name: "Bots", href: "/bots" },
    { id: "capacity", name: "Capacity", href: "/capacity" },
    { id: "charter", name: "Charter", href: "/charter" },
    { id: "usage", name: "Usage", href: "/usage" },
    { id: "costs", name: "Costs", href: "/costs" },
    { id: "trends", name: "Trends", href: "/trends" },
    { id: "instructions", name: "Instructions", href: "/instructions" },
    { id: "knowledge", name: "Knowledge", href: "/knowledge" },
    { id: "files", name: "Files", href: "/files" },
    { id: "audit", name: "Audit", href: "/audit" },
    { id: "settings", name: "Settings", href: "/settings" },
    { id: "logs", name: "Logs", href: "/logs" },
  ];
  const tabs = ["swarm", "briefs", "agents"];
  const more = [
    ...nav.filter((item) => !tabs.includes(item.id)),
    { id: "plans", name: "Plans", href: "/plans" },
    { id: "questions", name: "Questions", href: "/questions" },
  ];
  const leave = () => { setInboxOpen(false); setMoreOpen(false); };

  return (
    <Clock.Provider value={now}>
      <header className="topbar" ref={topbar}>
        <Link className="brand" href="/"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
        <nav aria-label="Workspace">
          {nav.map((item) => (
            <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined}>
              {item.name}
            </Link>
          ))}
        </nav>
        <div className="top-actions">
          <ConnectionStatus status={status} />
          <button type="button" aria-expanded={inboxOpen} onClick={() => setInboxOpen(!inboxOpen)}>
            Decisions <span className="nav-count">{decisionCount}</span>
          </button>
          <button className={state.data?.paused ? "primary" : "danger"} data-write onClick={togglePause} disabled={pending || !state.data}>
            {state.data?.paused ? "Resume all" : "Pause all"}
          </button>
          <button className="desktop-only" onClick={signOut}>Sign out</button>
        </div>
      </header>
      <main className={`workspace ${section === "swarm" ? "wide" : ""}`}>
        <Feedback error={error} />
        <ResourceState error={state.error} retry={state.refresh} />
        {state.data?.paused && <div className="notice" role="status">All agents paused{state.data.pause_reason && ` · ${state.data.pause_reason}`}</div>}
        <div className={`workspace-layout ${inboxOpen ? "with-decisions" : ""}`}>
          <div className="stack workspace-content">
            {section === "swarm" && !briefID && <Onboarding revisions={revisions} />}
            {section === "swarm" && <SwarmOverview id={briefID} architecture={architecture} revisions={revisions} recent={recent} openThread={setThread} />}
            {section === "swarm" && <OutputTile />}
            {section === "briefs" && <BriefsView revision={revisions.briefs} />}
            {section === "agents" && <AgentsView revision={revisions.agents} openThread={setThread} />}
            {section === "agents" && <Leaderboard revision={revisions.board} />}
            {section === "bots" && <BotsView revision={revisions.agents} openThread={setThread} />}
            {section === "plans" && <PlansView revision={revisions.plans} openThread={setThread} />}
            {section === "questions" && <QuestionsView revision={revisions.questions} openThread={setThread} />}
            {section === "capacity" && <CapacityView revision={revisions.capacity} />}
            {section === "charter" && <CharterView />}
            {section === "usage" && <UsageView revision={revisions.capacity} />}
            {section === "costs" && <CostsView revision={revisions.board} />}
            {section === "trends" && <TrendsView revision={revisions.board} />}
            {section === "files" && <FilesView />}
            {section === "settings" && <SettingsView revisions={revisions} />}
          </div>
          {inboxOpen && <DecisionInbox feed={decisions.data} error={decisions.error} count={decisionCount}
            revision={revisions.decisions} refresh={decisions.refresh} close={() => setInboxOpen(false)} openThread={setThread} />}
        </div>
      </main>
      <nav className="tabbar" aria-label="Primary">
        <button type="button" aria-pressed={inboxOpen} onClick={() => { setMoreOpen(false); setInboxOpen(!inboxOpen); window.scrollTo({ top: 0 }); }}>
          Inbox<span className="nav-count">{decisionCount}</span>
        </button>
        {nav.filter((item) => tabs.includes(item.id)).map((item) => (
          <Link key={item.id} href={item.href} aria-current={section === item.id && !inboxOpen ? "page" : undefined} onClick={leave}>
            {item.name}
          </Link>
        ))}
        <button type="button" aria-expanded={moreOpen} aria-current={!tabs.includes(section) && !inboxOpen ? "page" : undefined} onClick={() => setMoreOpen(!moreOpen)}>
          More
        </button>
      </nav>
      {moreOpen && <div className="more-sheet" role="dialog" aria-label="More">
        {more.map((item) => (
          <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined} onClick={leave}>{item.name}</Link>
        ))}
        <button type="button" onClick={signOut}>Sign out</button>
      </div>}
      {thread && <ThreadPanel key={`${thread.kind}:${thread.id}`} thread={thread} revision={revisions.thread} top={topbarHeight} close={() => setThread(null)} />}
      <CommandPalette />
      <KeyboardShortcuts />
    </Clock.Provider>
  );
}
