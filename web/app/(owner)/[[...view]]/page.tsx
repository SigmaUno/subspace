import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { ownerCookie, ownerStatus } from "../../../lib/upstream";
import { HillclimbPage } from "./hillclimb";
import { PromptsPage } from "./prompts";
import { Workspace } from "./workspace";

export default async function Page({ params }: PageProps<"/[[...view]]">) {
  const { view = [] } = await params;
  const token = (await cookies()).get(ownerCookie)?.value;
  if (!token) redirect("/login");
  const status = await ownerStatus(token).catch(() => 502);
  if (status === 401 || status === 403) redirect("/login");
  if (status !== 200) redirect("/login?error=unavailable");

  const [root, id, subview] = view;
  if (root === "prompts" && view.length === 1) return <PromptsPage />;
  if (root === "hillclimb" && view.length === 1) return <HillclimbPage />;
  const architecture = subview === "architecture";
  if (root === "briefs" && id && (view.length === 2 || view.length === 3 && architecture)) {
    return <Workspace section="swarm" briefID={id} architecture={architecture} />;
  }
  if (view.length > 1 || root && !["briefs", "agents", "plans", "questions", "capacity", "usage"].includes(root)) notFound();
  return <Workspace section={root || "swarm"} architecture={false} />;
}
