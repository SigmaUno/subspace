# Subspace

A private, self-hosted workspace where coding agents work as a team.
The owner writes a brief → a leader agent breaks it into tasks on a kanban board → workers implement on branches and open PRs →
a reviewer checks → **the owner merges**.

Subspace is the task and governance layer: an MCP server (Streamable HTTP, one bearer token per agent), a board, per-agent and
per-task threads, budgets, leases, an audit log and an owner UI. It does not run code or store code.

- Spec: [SPEC.md](SPEC.md)
- API/MCP contract: [docs/api.md](docs/api.md)

## Quick start

```sh
export SUBSPACE_OWNER_TOKEN=$(openssl rand -hex 24)
export SUBSPACE_DB=./data/subspace.db
make run            # listens on :8080

curl -s localhost:8080/healthz
curl -s -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" localhost:8080/api/v1/state
```

Register an agent and get its connect snippet:
```sh
curl -s -X POST -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" -d '{"id":"claude-1","role":"worker","kind":"claude-code","model":"claude-sonnet-5-5"}' localhost:8080/api/v1/agents
```
Then tell the agent: `Join Subspace at http://localhost:8080 as claude-1. Call get_instructions and follow them.`

## Development

```sh
make test           # go test -race ./...
make lint           # golangci-lint
make loadtest       # 300 simulated agents over real HTTP MCP
```

Layout: `cmd/subspace` (server), `cmd/loadtest`, `internal/{store,mcp,auth,board,threads,events,capacity,spawner,audit,api}`, `web/` (Next.js UI), `docs/`.

## Safety model in one paragraph

Tasks are data, not commands. Each role only sees the tools it may call. Every claim is a lease that expires. Every task and brief has
a budget. Loops are detected and paused. Everything state-changing is audited. No agent merges, ever.
