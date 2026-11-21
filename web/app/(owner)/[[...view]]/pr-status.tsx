"use client";

import { useMemo } from "react";
import { optionalResource, safeURL, useResource } from "../../../lib/client";
import type { PRList, PRStatus } from "../../../lib/pr-types";

const stateTone = { open: "in_progress", merged: "done", closed: "cancelled", "": "backlog" } as const;
const checksTone = { success: "approved", failure: "blocked", pending: "changes_requested", none: "backlog" } as const;

export function usePRs(revision: number, briefID: string) {
  const query = new URLSearchParams({ brief_id: briefID, limit: "1000" });
  const feed = useResource(`prs?${query}`, revision, optionalResource<PRList>);
  const supported = feed.data !== null;
  const byTask = useMemo(() => new Map((feed.data?.prs ?? []).map((pr) => [pr.task_id, pr])), [feed.data]);
  return { supported, byTask };
}

export function usePR(taskID: string, revision: number) {
  return useResource(`tasks/${encodeURIComponent(taskID)}/pr`, revision, optionalResource<PRStatus>);
}

function mergeable(pr: PRStatus) {
  if (pr.state !== "open") return undefined;
  return pr.mergeable === null ? "Mergeability pending" : pr.mergeable ? "Mergeable" : "Conflicts";
}

export function PRBadges({ pr }: { pr: PRStatus }) {
  const url = safeURL(pr.url);
  const merge = mergeable(pr);
  return <div className="actions" aria-label="Pull request status">
    <span className="pill" data-status={stateTone[pr.state] ?? "backlog"}>{pr.state ? `PR ${pr.state}${pr.draft ? " · draft" : ""}` : "PR syncing"}</span>
    <span className="pill" data-status={checksTone[pr.checks] ?? "backlog"}>Checks {pr.checks}</span>
    {merge && <span className="pill" data-status={pr.mergeable ? "approved" : pr.mergeable === false ? "blocked" : "backlog"}>{merge}</span>}
    {pr.merge_ready && <span className="pill" data-status="approved">Merge ready</span>}
    {pr.head_sha && <code className="meta" title={pr.head_sha}>{pr.head_sha.slice(0, 7)}</code>}
    {url && <a href={url} target="_blank" rel="noreferrer">{pr.repo}#{pr.number}</a>}
  </div>;
}
