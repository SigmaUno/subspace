---
title: "Experiment: {{title}}"
repos: ["{{repo}}"]
max_cost_eur: 60
max_parallel_agents: 4
allowed_providers: []
cross_review: false
playbook: research-spike
task_budget:
  max_minutes: 180
  max_cost_eur: 6
---
# Hypothesis

{{hypothesis}}

## Measurement

{{measurement}}

## Acceptance criteria

- Each experiment can be reproduced from a clean checkout with one documented command.
- Results, including negative ones, are written up in the pull request.
