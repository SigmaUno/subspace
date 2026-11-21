---
title: "Security audit: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 40
max_parallel_agents: 3
allowed_providers: []
cross_review: true
task_budget:
  max_minutes: 120
  max_cost_eur: 5
---
# Security audit

Scope: {{scope}}

## Focus

- Authentication and authorization checks on every entry point.
- Input validation, size limits and resource exhaustion.
- Secrets handling: nothing sensitive in logs, errors or commits.
- Injection: SQL, shell, path traversal, template rendering.

## Deliverables

- One task per finding, with severity, a reproduction and a fix.
- A regression test for every fixed finding.
- Findings that need an owner decision are raised with ask_owner.
