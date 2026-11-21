"use client";

import Link from "next/link";
import { useState } from "react";
import { optionalResource, useResource } from "../../../../lib/client";
import type { Control, Thread } from "../../../../lib/types";
import { ThreadPanel } from "../thread";
import { useLive } from "../shared/live";
import { TopNav } from "../shell";
import { DecisionInbox, type DecisionFeed } from "../decisions";
import { IncidentBanner } from "../incident";
import { CommandPalette } from "../search";
import { KeyboardShortcuts } from "../shortcuts";
import { Clock, Feedback } from "../shared/components";
import { ConnectionStatus, ResourceState } from "../shared/states";
import { useNow, useOwnerControls, useTopbarHeight } from "./hooks";
import { MobileNav } from "./mobile-nav";
import { Sections } from "./sections";

export function Workspace({ section, briefID, architecture }: { section: string; briefID?: string; architecture: boolean }) {
  const { topbar, height: topbarHeight } = useTopbarHeight();
  const now = useNow();
  const [thread, setThread] = useState<Thread | null>(null);
  const [inboxOpen, setInboxOpen] = useState(section === "swarm");
  const [moreOpen, setMoreOpen] = useState(false);
  const { revisions, recent, status } = useLive("all");
  const state = useResource<Control>("state", revisions.control);
  const decisions = useResource("decisions", revisions.decisions, optionalResource<DecisionFeed>);
  const decisionCount = decisions.data?.count ?? decisions.data?.decisions?.length ??
    ((state.data?.counts?.open_questions ?? 0) + (state.data?.counts?.proposed_plans ?? 0));
  const { pending, error, togglePause, signOut } = useOwnerControls(state);

  return (
    <Clock.Provider value={now}>
      <header className="topbar" ref={topbar}>
        <Link className="brand" href="/"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
        <TopNav section={section} />
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
        <IncidentBanner revision={revisions.control} />
        {state.data?.paused && <div className="notice" role="status">All agents paused{state.data.pause_reason && ` · ${state.data.pause_reason}`}</div>}
        <div className={`workspace-layout ${inboxOpen ? "with-decisions" : ""}`}>
          <div className="stack workspace-content">
            <Sections section={section} briefID={briefID} architecture={architecture} revisions={revisions} recent={recent} openThread={setThread} />
          </div>
          {inboxOpen && <DecisionInbox feed={decisions.data} error={decisions.error} count={decisionCount}
            revision={revisions.decisions} refresh={decisions.refresh} close={() => setInboxOpen(false)} openThread={setThread} />}
        </div>
      </main>
      <MobileNav section={section} inboxOpen={inboxOpen} moreOpen={moreOpen} decisionCount={decisionCount}
        setInboxOpen={setInboxOpen} setMoreOpen={setMoreOpen} signOut={signOut} />
      {thread && <ThreadPanel key={`${thread.kind}:${thread.id}`} thread={thread} revision={revisions.thread} top={topbarHeight} close={() => setThread(null)} />}
      <CommandPalette />
      <KeyboardShortcuts />
    </Clock.Provider>
  );
}
