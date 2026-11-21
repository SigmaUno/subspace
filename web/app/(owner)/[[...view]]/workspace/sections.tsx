"use client";

import type { Message, Thread } from "../../../../lib/types";
import { BotsView } from "../bots";
import { BriefsView } from "../board";
import { CharterView } from "../charter";
import { AgentsView, CapacityView, PlansView, QuestionsView } from "../operations";
import { HelpRequestsView } from "../helprequests";
import { Leaderboard } from "../leaderboard";
import { TrendsView } from "../trends";
import { UsageView } from "../usage";
import { CostsView } from "../costs";
import type { Revisions } from "../shared/live";
import { SettingsView } from "../settings";
import { OutputTile } from "../terminal";
import { FilesView } from "../files";
import { Onboarding } from "../onboarding";
import { SwarmOverview } from "../overview";

type Props = {
  section: string;
  briefID?: string;
  architecture: boolean;
  revisions: Revisions;
  recent: Record<string, Message>;
  openThread: (thread: Thread) => void;
};

export function Sections({ section, briefID, architecture, revisions, recent, openThread }: Props) {
  return <>
    {section === "swarm" && !briefID && <Onboarding revisions={revisions} />}
    {section === "swarm" && <SwarmOverview id={briefID} architecture={architecture} revisions={revisions} recent={recent} openThread={openThread} />}
    {section === "swarm" && <OutputTile />}
    {section === "briefs" && <BriefsView revision={revisions.briefs} />}
    {section === "agents" && <AgentsView revision={revisions.agents} openThread={openThread} />}
    {section === "agents" && <Leaderboard revision={revisions.board} />}
    {section === "bots" && <BotsView revision={revisions.agents} openThread={openThread} />}
    {section === "plans" && <PlansView revision={revisions.plans} openThread={openThread} />}
    {section === "questions" && <QuestionsView revision={revisions.questions} openThread={openThread} />}
    {section === "help" && <HelpRequestsView revision={revisions.board} />}
    {section === "capacity" && <CapacityView revision={revisions.capacity} />}
    {section === "charter" && <CharterView />}
    {section === "usage" && <UsageView revision={revisions.capacity} />}
    {section === "costs" && <CostsView revision={revisions.board} />}
    {section === "trends" && <TrendsView revision={revisions.board} />}
    {section === "files" && <FilesView />}
    {section === "settings" && <SettingsView revisions={revisions} />}
  </>;
}
