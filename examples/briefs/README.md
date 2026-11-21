# Sample briefs

This directory contains five realistic sample briefs, one JSON file each. Every file is exactly the request body for
`POST /api/v1/briefs` (see [docs/api.md](../../docs/api.md) §3, "Briefs"), so each can be loaded straight into a running
Subspace server.

| File | Kind | Provider | Budget (EUR) |
|---|---|---|---|
| `01-go-cli-json-output.json` | small Go CLI feature | deepseek | 30 |
| `02-web-board-page.json` | Next.js page | openai, anthropic | 60 |
| `03-fix-lease-stale-heartbeat.json` | bugfix with repro | deepseek | 25 |
| `04-docs-deploy-runbook.json` | docs task | anthropic | 15 |
| `05-webhook-pr-status-sync.json` | multi-task feature with dependencies | all | 120 |

## Body shape

Each file matches the create-brief request body:

```jsonc
{
  "title": "…",
  "body": "markdown brief the leader breaks into tasks",
  "repos": ["SigmaUno/subspace"],
  "max_cost_eur": 50,           // brief spend cap, >= 0
  "max_parallel_agents": 10,    // 0 means the server default (10)
  "allowed_providers": []       // [] = any provider; otherwise anthropic/openai/deepseek
}
```

The multi-task sample (`05-…`) still uses this same body shape; its task breakdown and dependency order are described in
the `body` markdown, which is what a leader agent reads when it proposes a plan.

## Loading with curl

Start the server first (see the top-level [README.md](../../README.md)), then `POST` each file:

```sh
export SUBSPACE_URL=http://localhost:8080
export SUBSPACE_OWNER_TOKEN="$(openssl rand -hex 24)"   # or your real token

for f in examples/briefs/*.json; do
  curl -sS -X POST "$SUBSPACE_URL/api/v1/briefs" \
    -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" \
    -H "Content-Type: application/json" \
    --data-binary "@$f"
  echo
done
```

A single brief:

```sh
curl -sS -X POST "$SUBSPACE_URL/api/v1/briefs" \
  -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @examples/briefs/01-go-cli-json-output.json
```

Each call returns the created `Brief` (HTTP 201).

## Loading with subspacectl

`subspacectl` is the owner CLI (SPEC §12, built alongside the server). It talks to the same `/api/v1` endpoint, so the
same JSON files are valid input:

```sh
subspacectl brief create --file examples/briefs/01-go-cli-json-output.json
# or pipe it:
cat examples/briefs/02-web-board-page.json | subspacectl brief create -
```

Point it at your server with the usual env/config (for example `SUBSPACE_URL` / the owner token). The exact flag names
follow whatever `subspacectl` ships; run `subspacectl brief create --help` to confirm.

## What happens next

1. The owner creates the brief (above) — it lands with status `open`, `plan_status` `none`.
2. A leader agent proposes a plan of tasks for the brief (`propose_plan`).
3. The owner approves the plan; tasks move `backlog -> ready`, then get assigned, claimed, worked, reviewed, and merged.
