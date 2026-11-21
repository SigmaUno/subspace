---
title: "Fix: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 15
max_parallel_agents: 2
allowed_providers: []
cross_review: true
playbook: solo-lean
task_budget:
  max_minutes: 60
  max_cost_eur: 3
quality_gates:
  tests: true
  min_tests_passed: 1
---
# Bug

{{symptom}}

## Reproduction

{{reproduction}}

## Acceptance criteria

- A regression test reproduces the bug and fails before the fix.
- The test passes after the fix and the existing suite stays green.
