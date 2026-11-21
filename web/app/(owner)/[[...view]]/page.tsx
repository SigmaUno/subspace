import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { ownerCookie, ownerStatus } from "../../../lib/upstream";
import { AgentDetailPage } from "./agent-detail";
import { AuditPage } from "./audit";
import { DagPage } from "./dag";
import { HillclimbPage } from "./hillclimb";
import { MergeQueuePage } from "./mergequeue";
import { InstructionsPage } from "./instructions";
import { PromptsPage } from "./prompts";
import { ReportPage } from "./report";
import { TaskDetailPage } from "./task-detail";
import { Workspace } from "./workspace";

export default async function Page({ params }: PageProps<"/[[...view]]">) {
  const { view = [] } = await params;
  const token = (await cookies()).get(ownerCookie)?.value;
  if (!token) redirect("/login");
  const status = await ownerStatus(token).catch(() => 502);
  if (status === 401 || status === 403) redirect("/login");
  if (status !== 200) redirect("/login?error=unavailable");

  const [root, id, subview] = view;
  if (root === "audit" && view.length === 1) return <AuditPage />;
  if (root === "prompts" && view.length === 1) return <PromptsPage />;
  if (root === "hillclimb" && view.length === 1) return <HillclimbPage />;
  if (root === "mergequeue" && view.length === 1) return <MergeQueuePage />;
  if (root === "instructions" && view.length === 1) return <InstructionsPage />;
  if (root === "charter" && view.length === 1) return <Workspace section="charter" architecture={false} />;
  if (root === "briefs" && id && subview === "report" && view.length === 3) return <ReportPage id={id} />;
  if (root === "briefs" && id && subview === "graph" && view.length === 3) return <DagPage id={id} />;
  if (root === "agents" && id && view.length === 2) return <AgentDetailPage id={id} />;
  if (root === "tasks" && id && view.length === 2) return <TaskDetailPage id={id} />;
  const architecture = subview === "architecture";
  if (root === "briefs" && id && (view.length === 2 || view.length === 3 && architecture)) {
    return <Workspace section="swarm" briefID={id} architecture={architecture} />;
  }
  if (view.length > 1 || root && !["briefs", "agents", "bots", "plans", "questions", "capacity", "usage", "trends", "files"].includes(root)) notFound();
  return <Workspace section={root || "swarm"} architecture={false} />;
}
