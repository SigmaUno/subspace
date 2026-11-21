---
title: "Fix: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 25
max_parallel_agents: 2
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 90
  max_cost_eur: 4
---
# Bug

{{symptom}}

## Reproduction

{{reproduction}}

## Expected

{{expected}}

## Acceptance criteria

- A regression test reproduces the bug and fails before the fix.
- The test passes after the fix and the full suite stays green.
- The root cause is described in the pull request.
