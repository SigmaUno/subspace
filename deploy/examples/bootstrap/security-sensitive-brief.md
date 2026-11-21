---
title: "Reject webhook payloads with invalid signatures"
repos: ["example-org/payments"]
max_cost_eur: 25
max_parallel_agents: 2
allowed_providers: ["anthropic", "openai"]
cross_review: true
playbook: security-strict
task_budget:
  max_minutes: 90
  max_cost_eur: 4
quality_gates:
  tests: true
  min_tests_passed: 2
  review: true
---
# Issue

The payment webhook handler logs a warning for an invalid signature but still processes the payload.

## Constraints

- Do not put real signing keys or payloads from production into code, tests, tasks or notes. Use generated test keys.
- Compare signatures in constant time.

## Acceptance criteria

- A payload with a missing or wrong signature is rejected with 401 and has no side effects.
- Negative tests cover a missing header, a wrong signature, a replayed timestamp and a truncated body.
- A rollback task describes how to revert the change safely.
