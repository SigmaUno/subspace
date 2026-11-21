"use client";

import { api, useSubmit } from "../../../../lib/client";
import type { Backup, BackupList } from "../../../../lib/settings-types";
import { Feedback } from "../shared/components";
import { formatSize, formatTime } from "../shared/format";
import { EmptyState } from "../shared/states";
import { useToast } from "../shared/ui";

export function BackupsPanel({ list, refresh }: { list: BackupList; refresh: () => void }) {
  const form = useSubmit();
  const toast = useToast();
  const backups: Backup[] = list.backups ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>Backups</h2></div>
    <form className="stack" onSubmit={(event) => form.submit(event, async () => {
      const result = await api<{ path: string; bytes: number }>("backup", "POST", {});
      toast(`Backup created: ${result.path.split("/").pop()}.`);
      refresh();
    })}>
      <div className="actions"><button className="primary" disabled={form.busy}>Create backup now</button></div>
      <Feedback error={form.error} />
    </form>
    {!backups.length && <EmptyState>No backups yet.</EmptyState>}
    {!!backups.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Created</th><th>Size</th><th>Integrity</th></tr></thead>
        <tbody>{backups.map((backup) => (
          <tr key={backup.name}>
            <th scope="row">{backup.name}</th>
            <td>{formatTime(backup.created_at)}</td>
            <td>{formatSize(backup.bytes)}</td>
            <td><span className={`pill ${backup.verified ? "active" : "revoked"}`}>{backup.verified ? "verified" : "failed"}</span></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
