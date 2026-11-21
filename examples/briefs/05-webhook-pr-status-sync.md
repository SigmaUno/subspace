---
title: "Webhooks: PR status sync into the board"
repos: ["SigmaUno/subspace"]
max_cost_eur: 120
max_parallel_agents: 8
allowed_providers: ["anthropic", "openai", "deepseek"]
task_budget:
  max_minutes: 120
  max_cost_eur: 10
components:
  - name: store
    description: repo and secret linking table
    paths: [internal/store]
  - name: auth
    description: bot user credentials for the git host
    paths: [internal/auth]
    depends_on: [store]
  - name: webhook
    description: signed PR event handler and status mapping
    paths:
      - internal/webhook
      - internal/server
    depends_on: [auth]
---
Add webhook support so a Forgejo/GitHub PR opened or merged updates the matching task (open -> submitted; merge -> prompt the owner to mark done). This is a multi-task feature; the leader should break it into the following tasks with these dependencies.

Tasks:
1. Store: add a repo/secret linking table and its store methods (no deps).
2. Auth: bot user credentials for the git host, stored and rotated like agent tokens (depends on 1).
3. Handler: an endpoint that receives PR events, verifies the signature, and maps the branch (`sub/<task-id>-<slug>`) back to the task (depends on 2).
4. Status mapping: PR opened -> `submitted`; PR merged -> notify the owner to mark `done` (depends on 3).
5. Config and docs: wire the webhook URL and secret, and document setup (depends on 4).

Acceptance criteria:
- A signed PR-opened event marks the task `submitted`.
- A merge event posts an owner notification; nothing is auto-merged (SPEC §1 rule 5).
- Invalid signatures are rejected and logged.
