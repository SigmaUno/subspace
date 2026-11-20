import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { ownerCookie, ownerStatus } from "../../../lib/upstream";
import { Architecture } from "./architecture";
import { ChatScreen } from "./chat";
import { Decisions } from "./decisions";
import { HillclimbPage } from "./hillclimb";
import { Overview } from "./overview";
import { PromptsPage } from "./prompts";
import { Workspace } from "./workspace";

export default async function Page({ params }: PageProps<"/[[...view]]">) {
  const { view = [] } = await params;
  const token = (await cookies()).get(ownerCookie)?.value;
  if (!token) redirect("/login");
  const status = await ownerStatus(token).catch(() => 502);
  if (status === 401 || status === 403) redirect("/login");
  if (status !== 200) redirect("/login?error=unavailable");

  const section = view[0] || "briefs";
  if (!["briefs", "agents", "plans", "questions", "capacity", "prompts", "hillclimb", "decisions", "chat", "architecture"].includes(section) ||
    (section === "briefs" || section === "architecture" ? view.length > 2 : view.length > 1)) notFound();
  if (!view.length) return <Overview />;
  if (section === "prompts") return <PromptsPage />;
  if (section === "hillclimb") return <HillclimbPage />;
  if (section === "decisions") return <Decisions />;
  if (section === "chat") return <ChatScreen />;
  if (section === "architecture" && view.length === 2) return <Architecture briefID={view[1]} />;
  return <Workspace section={section} briefID={view[1]} />;
}
