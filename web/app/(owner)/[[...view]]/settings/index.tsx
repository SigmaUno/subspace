"use client";

import { useState, type ReactNode } from "react";
import { optionalResource, useResource } from "../../../../lib/client";
import type {
  About, AlertList, BackupList, BreakerList, NotifyStatus, RetentionStatus, TokenList, WebhookList,
} from "../../../../lib/settings-types";
import type { DigestPreview } from "../../../../lib/digest-types";
import type { Control } from "../../../../lib/types";
import type { Revisions } from "../shared/live";
import { PageHeading, ResourceState } from "../shared/states";
import { CapacityView } from "../operations";
import { CharterView } from "../charter";
import { InstructionsView } from "../instructions";
import { OrgBudget } from "../org-budget";
import { PlaybooksPanel } from "../playbooks";
import { AboutPanel } from "./about";
import { AlertsPanel } from "./alerts";
import { AnnouncementsView } from "./announcements";
import { BackupsPanel } from "./backups";
import { BreakersPanel } from "./breakers";
import { DigestPanel } from "./digest";
import { NotificationsPanel } from "./notifications";
import { PushPanel } from "./push";
import { RepositoriesView } from "./repos";
import { RetentionPanel } from "./retention";
import { SchedulesView } from "./schedules";
import { TokensPanel } from "./tokens";
import { WebhooksPanel } from "./webhooks";

function Loaded<T>({ resource, rows, children }: {
  resource: { data?: T | null; error: string; refresh: () => void };
  rows: number;
  children: (data: T) => ReactNode;
}) {
  return <>
    <ResourceState error={resource.error} loading={resource.data === undefined} retry={resource.refresh} rows={rows} />
    {resource.data && children(resource.data)}
  </>;
}

export function SettingsView({ revisions }: { revisions: Revisions }) {
  const state = useResource<Control>("state", revisions.control);
  const notify = useResource("notify/status", revisions.control, optionalResource<NotifyStatus>);
  const backups = useResource("backups", 0, optionalResource<BackupList>);
  const tokens = useResource("tokens", 0, optionalResource<TokenList>);
  const alerts = useResource("alerts", revisions.control, optionalResource<AlertList>);
  const retention = useResource("retention", 0, optionalResource<RetentionStatus>);
  const webhooks = useResource("webhooks-out", 0, optionalResource<WebhookList>);
  const breakers = useResource("breakers", revisions.control, optionalResource<BreakerList>);
  const about = useResource("about", 0, optionalResource<About>);
  const [range, setRange] = useState("24h");
  const digest = useResource(`digest/preview?window=${range}`, 0, optionalResource<DigestPreview>);
  const [selected, setSelected] = useState("charter");

  const tabs = [
    { id: "charter", name: "Charter", show: true },
    { id: "capacity", name: "Capacity and limits", show: true },
    { id: "repositories", name: "Repositories", show: true },
    { id: "instructions", name: "Instructions", show: true },
    { id: "playbooks", name: "Playbooks", show: true },
    { id: "announcements", name: "Announcements", show: true },
    { id: "schedules", name: "Schedules", show: true },
    { id: "notifications", name: "Notifications", show: notify.data !== null },
    { id: "backups", name: "Backups", show: backups.data !== null },
    { id: "digest", name: "Digest", show: digest.data !== null },
    { id: "tokens", name: "Tokens", show: tokens.data !== null },
    { id: "alerts", name: "Alerts", show: alerts.data !== null },
    { id: "retention", name: "Retention", show: retention.data !== null },
    { id: "webhooks", name: "Webhooks", show: webhooks.data !== null },
    { id: "breakers", name: "Provider breakers", show: breakers.data !== null },
    { id: "about", name: "About", show: true },
  ].filter((tab) => tab.show);
  const active = tabs.some((tab) => tab.id === selected) ? selected : "charter";

  return <>
    <PageHeading title="Settings" />
    <div className="actions" role="group" aria-label="Settings">
      {tabs.map((tab) => <button key={tab.id} className="text-button" aria-pressed={tab.id === active}
        onClick={() => setSelected(tab.id)}>{tab.name}</button>)}
    </div>
    {active === "charter" && <CharterView embedded />}
    {active === "capacity" && <>
      <CapacityView revision={revisions.capacity} embedded />
      <OrgBudget revision={revisions.capacity} />
    </>}
    {active === "repositories" && <RepositoriesView />}
    {active === "instructions" && <InstructionsView embedded />}
    {active === "playbooks" && <PlaybooksPanel />}
    {active === "announcements" && <AnnouncementsView embedded />}
    {active === "schedules" && <SchedulesView embedded />}
    {active === "notifications" && <>
      <ResourceState error={notify.error} loading={notify.data === undefined} retry={notify.refresh} rows={2} />
      {notify.data && <NotificationsPanel status={notify.data} />}
      <PushPanel />
    </>}
    {active === "backups" && <Loaded resource={backups} rows={3}>{(data) => <BackupsPanel list={data} refresh={backups.refresh} />}</Loaded>}
    {active === "digest" && <Loaded resource={digest} rows={4}>{(data) => <DigestPanel preview={data} range={range} setRange={setRange} />}</Loaded>}
    {active === "tokens" && <Loaded resource={tokens} rows={3}>{(data) => <TokensPanel list={data} refresh={tokens.refresh} />}</Loaded>}
    {active === "alerts" && <Loaded resource={alerts} rows={3}>{(data) => <AlertsPanel list={data} />}</Loaded>}
    {active === "retention" && <Loaded resource={retention} rows={3}>{(data) => <RetentionPanel status={data} refresh={retention.refresh} />}</Loaded>}
    {active === "webhooks" && <Loaded resource={webhooks} rows={3}>{(data) => <WebhooksPanel list={data} refresh={webhooks.refresh} />}</Loaded>}
    {active === "breakers" && <Loaded resource={breakers} rows={3}>{(data) => <BreakersPanel list={data} refresh={breakers.refresh} />}</Loaded>}
    {active === "about" && <>
      <ResourceState error={state.error} loading={state.data === undefined} retry={state.refresh} rows={2} />
      <AboutPanel version={state.data?.version} about={about.data} />
    </>}
  </>;
}
