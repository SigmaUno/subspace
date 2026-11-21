---
title: "Paginate the audit log API"
repos: ["example-org/backend", "example-org/frontend"]
max_cost_eur: 60
max_parallel_agents: 4
allowed_providers: ["anthropic", "deepseek"]
cross_review: true
playbook: team-standard
task_budget:
  max_minutes: 120
  max_cost_eur: 5
quality_gates:
  tests: true
  min_tests_passed: 1
  review: true
---
# Goal

`GET /api/v1/audit` returns every row at once. Add cursor pagination to the backend and use it in the audit page of the frontend.

## Acceptance criteria

- The backend accepts `limit` (default 50, max 200) and `cursor`, and returns `next_cursor` when more rows exist.
- An integration test pages through a seeded log and sees every row exactly once.
- The frontend audit page loads the next page on demand and keeps its scroll position.
- Existing tests stay green.

## Notes for the leader

- Backend first; the frontend task depends on it.
