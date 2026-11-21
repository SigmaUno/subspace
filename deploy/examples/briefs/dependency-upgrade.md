---
title: "Upgrade {{package}} to {{version}}"
repos: ["{{repo}}"]
max_cost_eur: 12
max_parallel_agents: 1
allowed_providers: []
cross_review: true
playbook: oss-contribution
task_budget:
  max_minutes: 60
  max_cost_eur: 3
quality_gates:
  tests: true
  checks: true
---
# Goal

Upgrade `{{package}}` to `{{version}}` and fix whatever the upgrade breaks.

## Acceptance criteria

- The build, linters and the full test suite pass on the new version.
- The changelog entry lists the upgrade and any behaviour change that users can notice.
- No other dependency changes unless the upgrade requires them; name each one in the pull request.
