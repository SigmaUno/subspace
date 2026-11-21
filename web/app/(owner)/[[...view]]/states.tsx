"use client";

import type { ReactNode } from "react";
import type { LiveStatus } from "./live";

const connectionLabels: Record<LiveStatus, string> = {
  connected: "Live",
  reconnecting: "Reconnecting",
  offline: "Offline",
};

export function ConnectionStatus({ status }: { status: LiveStatus }) {
  return <span className={`connection ${status}`} role="status">{connectionLabels[status]}</span>;
}

export function Skeleton({ rows = 3, label = "Loading" }: { rows?: number; label?: string }) {
  return <div className="skeleton stack" role="status" aria-label={label}>
    {Array.from({ length: rows }, (_, index) => <span key={index} className="skeleton-line" style={{ width: `${100 - index * 12}%` }} />)}
  </div>;
}

export function ErrorBanner({ message, retry }: { message: string; retry?: () => void }) {
  return <div className="error error-banner" role="alert">
    <span>{message}</span>
    {retry && <button type="button" onClick={retry}>Retry</button>}
  </div>;
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export function ResourceState({ error, loading, retry, rows }: { error?: string; loading?: boolean; retry?: () => void; rows?: number }) {
  if (error) return <ErrorBanner message={error} retry={retry} />;
  return loading ? <Skeleton rows={rows} /> : null;
}

export function PageHeading({ title, embedded, children }: { title: string; embedded?: boolean; children?: ReactNode }) {
  const Title = embedded ? "h2" : "h1";
  return <div className="heading"><Title>{title}</Title>{children}</div>;
}
