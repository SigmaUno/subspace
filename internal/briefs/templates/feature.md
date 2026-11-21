---
title: "{{title}}"
repos: ["{{repo}}"]
max_cost_eur: 50
max_parallel_agents: 5
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 120
  max_cost_eur: 5
---
# Goal

{{goal}}

## Scope

- Implement the feature end to end in `{{repo}}`.
- Keep existing behaviour unchanged outside the feature.

## Acceptance criteria

{{acceptance}}

## Notes for the leader

- Split the work into reviewable tasks with explicit dependencies.
- Every task ships with integration tests through the public API.
