---
title: "Docs: deployment and operations runbook"
repos: ["SigmaUno/subspace"]
max_cost_eur: 15
max_parallel_agents: 2
allowed_providers: ["anthropic"]
---
Write a deployment and operations runbook for self-hosting Subspace, placed under `docs/`. Cover: configuration env vars (SPEC §10), the docker compose / Caddy deployment path (SPEC §12), backups and restore, token management and rotation, and the health/liveness endpoints.

Acceptance criteria:
- A `docs/` markdown runbook covering the sections above.
- Every command shown is tested against a local run.
- No secrets or real tokens appear anywhere in the docs.

Notes for the leader:
- Keep it operator-focused and copy-paste friendly; skip agent internals.
- Cross-link to SPEC.md and docs/api.md rather than duplicating them.
- Include an example `docker compose up` and a restore-from-backup procedure.
