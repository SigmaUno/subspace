---
title: "Refactor: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 40
max_parallel_agents: 3
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 120
  max_cost_eur: 5
---
# Refactor

Target: `{{target}}`

## Motivation

{{motivation}}

## Constraints

- No behaviour change: existing tests must pass unmodified.
- Land the change in small, independently reviewable steps.
- Public APIs keep their contracts unless the brief says otherwise.

## Acceptance criteria

- The target code follows the new structure.
- Test coverage of the touched code does not drop.
