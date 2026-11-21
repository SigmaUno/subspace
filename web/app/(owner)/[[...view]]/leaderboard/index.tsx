"use client";

import { useResource } from "../../../../lib/client";
import type { AgentStats, AgentTrendPoint } from "../../../../lib/agent-stats-types";
import { agentDuration } from "../../../../lib/duration";
import { EntityLink } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";

const size = { width: 84, height: 24, pad: 3 };

function Trend({ trend, agent }: { trend: AgentTrendPoint[]; agent: string }) {
  const high = Math.max(...trend.map((point) => point.completed), 1);
  const x = (index: number) => trend.length > 1 ? size.pad + (index / (trend.length - 1)) * (size.width - 2 * size.pad) : size.width / 2;
  const y = (completed: number) => size.height - size.pad - (completed / high) * (size.height - 2 * size.pad);
  const total = trend.reduce((sum, point) => sum + point.completed, 0);
  return <svg className="hillclimb-chart sparkline" viewBox={`0 0 ${size.width} ${size.height}`} role="img"
    aria-label={`${agent}: ${total} tasks completed in the last ${trend.length} days`}>
    <polyline className="series" points={trend.map((point, index) => `${x(index)},${y(point.completed)}`).join(" ")} />
    {trend.map((point, index) => <circle key={point.day} className="point" cx={x(index)} cy={y(point.completed)} r={1.5}>
      <title>{point.day}: {point.completed} completed</title>
    </circle>)}
  </svg>;
}

export function Leaderboard({ revision }: { revision: number }) {
  const stats = useResource<{ agents: AgentStats[] }>("agent-stats", revision);
  const rows = stats.data?.agents ?? [];
  return <section className="stack" aria-labelledby="leaderboard-title">
    <h2 id="leaderboard-title">Leaderboard</h2>
    <ResourceState error={stats.error} loading={!stats.data} retry={stats.refresh} rows={3} />
    {stats.data && !rows.length && <EmptyState>No agent statistics yet.</EmptyState>}
    {!!rows.length && <div className="table-wrap" role="region" aria-label="Leaderboard" tabIndex={0}>
      <table>
        <thead><tr><th>#</th><th>Agent</th><th>Score</th><th>Done</th><th>Avg / p90</th><th>Rounds</th><th>First pass</th>
          <th>Blocked / expired</th><th>Cost / task</th><th>Tokens / task</th><th>7 days</th></tr></thead>
        <tbody>{rows.map((row, index) => (
          <tr key={row.agent_id}>
            <td>{index + 1}</td>
            <th scope="row"><EntityLink kind="agent" id={row.agent_id} />
              <div className="meta">{row.role} · {row.provider}</div></th>
            <td>{row.score.toFixed(2)}</td>
            <td>{row.tasks_completed}</td>
            <td>{agentDuration(row.avg_duration_seconds)} / {agentDuration(row.p90_duration_seconds)}</td>
            <td>{row.review_rounds_per_task.toFixed(2)}</td>
            <td>{row.first_pass_approval_rate === null ? "—" : `${Math.round(row.first_pass_approval_rate * 100)}%`}</td>
            <td className={row.blocked + row.expired ? "stale" : undefined}>{row.blocked} / {row.expired}</td>
            <td>{row.tasks_completed ? row.cost_per_task.toFixed(2) : "—"}</td>
            <td>{row.tokens_per_task ? Math.round(row.tokens_per_task).toLocaleString() : "—"}</td>
            <td><Trend trend={row.trend} agent={row.agent_id} /></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
