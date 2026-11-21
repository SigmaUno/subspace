---
title: "Fix: stale lease_expires_at after lease sweep"
repos: ["SigmaUno/subspace"]
max_cost_eur: 25
max_parallel_agents: 2
allowed_providers: ["deepseek"]
---
Bug: when the lease sweeper returns an expired `in_progress` task to `assigned`, the task keeps its old `lease_expires_at` value. The board then shows a stale lease deadline for a task that is no longer leased, and a fresh claim is not granted cleanly.

Reproduction:
1. Create a brief and a task; assign and claim the task (lease ~15 min).
2. Let the lease expire (or run with `SUBSPACE_LEASE=1s` and wait).
3. Watch the sweeper move the task back to `assigned`.
4. Observe that `lease_expires_at` still holds the old timestamp.

Expected: when a lease is released by the sweeper (and on `release_task`, `reassign`, and `pause_all`), `lease_expires_at` is cleared to null so the next claim gets a fresh lease.

Acceptance criteria:
- After a sweep, the task's `lease_expires_at` is null.
- The fix is covered by a regression test that exercises the sweep path.
- Lease grant duration logic is unchanged.
