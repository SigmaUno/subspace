import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { ownerCookie } from "../../../lib/upstream";
import { Workspace } from "./workspace";

export default async function Page({ params }: PageProps<"/[[...view]]">) {
  const { view = [] } = await params;
  if (!(await cookies()).has(ownerCookie)) redirect("/login");

  const section = view[0] || "briefs";
  if (!["briefs", "agents", "plans", "questions", "capacity"].includes(section) ||
    (section === "briefs" ? view.length > 2 : view.length > 1)) notFound();
  return <Workspace section={section} briefID={view[1]} />;
}
