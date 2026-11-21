---
title: "Security: {{title}}"
repos: ["{{repo}}"]
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
  checks: true
  scope: true
  questions: true
  review: true
  budget: true
  max_pr_age_hours: 48
---
# Issue

{{issue}}

## Constraints

- Do not put secrets, exploit payloads for live systems or personal data into tasks, notes, commits or pull requests.
- Do not weaken authentication, authorization or validation to make a test pass.

## Acceptance criteria

{{acceptance}}

## Notes for the leader

- Include a rollback task and a tester task with negative tests.
