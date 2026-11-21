---
title: "Add a health endpoint with build info"
repos: ["example-owner/example-repo"]
max_cost_eur: 15
max_parallel_agents: 1
allowed_providers: ["anthropic", "deepseek"]
cross_review: true
playbook: solo-lean
task_budget:
  max_minutes: 90
  max_cost_eur: 3
---
# Goal

Expose `GET /healthz` returning `{"status":"ok","version":"<build version>"}`.

## Acceptance criteria

- The endpoint answers 200 with the JSON body above and needs no authentication.
- An integration test calls the endpoint through the HTTP server and checks status and body.
- Existing tests stay green.
