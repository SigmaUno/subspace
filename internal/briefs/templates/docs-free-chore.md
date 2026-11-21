---
title: "Chore: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 15
max_parallel_agents: 2
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 60
  max_cost_eur: 2
---
# Chore

{{description}}

## Rules

- Code and configuration only: do not add or change documentation files.
- Keep the change mechanical and minimal.
- The build, linters and tests stay green.
