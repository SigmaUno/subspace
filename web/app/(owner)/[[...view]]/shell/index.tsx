"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { MoreMenu } from "./more-menu";
import { primaryNav } from "./nav";
import { ConnectionStatus } from "../shared/states";
import type { LiveStatus } from "../shared/live";

export function TopNav({ section }: { section: string }) {
  return <nav aria-label="Workspace">
    {primaryNav.map((item) => (
      <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined}>{item.name}</Link>
    ))}
    <MoreMenu section={section} />
  </nav>;
}

export function PageShell({ section, status, className = "workspace", children }: {
  section: string;
  status?: LiveStatus;
  className?: string;
  children: ReactNode;
}) {
  return <>
    <header className="topbar">
      <Link className="brand" href="/"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
      <TopNav section={section} />
      {status && <div className="top-actions"><ConnectionStatus status={status} /></div>}
    </header>
    <main className={className}>{children}</main>
  </>;
}
