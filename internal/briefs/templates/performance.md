---
title: "Performance: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 60
max_parallel_agents: 3
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 180
  max_cost_eur: 8
---
# Performance target

Make `{{target}}` faster. Goal: {{goal}}.

## Measurement

Benchmark command: `{{command}}`

1. Record a baseline (median and p99) before any change.
2. Profile and rank the costs; fix the largest first.
3. Keep a change only if the numbers improve.

## Acceptance criteria

- Before and after numbers are reported in each pull request.
- Behaviour is unchanged and the test suite stays green.
- A regression benchmark is added.
