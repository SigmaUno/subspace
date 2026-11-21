"use client";

import Link from "next/link";
import { memo } from "react";
import { useRouter } from "next/navigation";
import { useResource } from "../../../../lib/client";
import type { Brief, Message, Thread } from "../../../../lib/types";
import { IncidentButton } from "../incident";
import type { Revisions } from "../shared/live";
import { ResourceState } from "../shared/states";
import { SwarmProject } from "./project";

export const SwarmOverview = memo(function SwarmOverview({ id, architecture, revisions, recent, openThread }: {
  id?: string; architecture: boolean; revisions: Revisions; recent: Record<string, Message>;
  openThread: (thread: Thread) => void;
}) {
  const briefs = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revisions.briefs);
  const router = useRouter();
  const items = briefs.data?.briefs ?? [];
  const selected = id ?? (items.find((item) => item.status === "open") ?? items[0])?.id;
  return <>
    <div className="heading">
      <h1>Swarm overview</h1>
      <IncidentButton revision={revisions.control} />
      {!!items.length && <label className="project-picker">Project<select value={selected} onChange={(event) => router.push(`/briefs/${encodeURIComponent(event.target.value)}`)}>
        {items.map((brief) => <option key={brief.id} value={brief.id}>{brief.title}</option>)}
      </select></label>}
    </div>
    <ResourceState error={briefs.error} loading={!briefs.data && !selected} retry={briefs.refresh} />
    {!selected && briefs.data && <div className="panel stack"><p>No briefs yet.</p><Link href="/briefs">Create a brief</Link></div>}
    {selected && <SwarmProject key={selected} id={selected} architecture={architecture} revisions={revisions} recent={recent} openThread={openThread} />}
  </>;
});
