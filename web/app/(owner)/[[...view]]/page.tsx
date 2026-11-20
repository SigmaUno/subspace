import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { ownerCookie, ownerStatus } from "../../../lib/upstream";
import { HillclimbPage } from "./hillclimb";
import { Workspace } from "./workspace";

export default async function Page({ params }: PageProps<"/[[...view]]">) {
  const { view = [] } = await params;
  const token = (await cookies()).get(ownerCookie)?.value;
  if (!token) redirect("/login");
  const status = await ownerStatus(token).catch(() => 502);
  if (status === 401 || status === 403) redirect("/login");
  if (status !== 200) redirect("/login?error=unavailable");

  const section = view[0] || "briefs";
  if (!["briefs", "agents", "plans", "questions", "capacity", "hillclimb"].includes(section) ||
    (section === "briefs" ? view.length > 2 : view.length > 1)) notFound();
  if (section === "hillclimb") return <HillclimbPage />;
  return <Workspace section={section} briefID={view[1]} />;
}
