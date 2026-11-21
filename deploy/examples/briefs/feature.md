---
title: "{{title}}"
repos: ["{{repo}}"]
max_cost_eur: 40
max_parallel_agents: 3
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
  budget: true
---
# Goal

{{goal}}

## Acceptance criteria

{{acceptance}}

## Notes for the leader

- Split the work into reviewable tasks with explicit dependencies.
- Each task states how it is tested.
