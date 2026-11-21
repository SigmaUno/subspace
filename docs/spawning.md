# Spawning agents

How the leader gets worker and reviewer processes started. Contract details live in [docs/api.md](api.md) (§3 Capacity & spawns,
§5 `request_spawn`) and [SPEC.md](../SPEC.md) §8 (Spawner, Capacity) and §9 (worker kinds). This page is the operator view.

## 1. Who does what

- The **leader** calls MCP `request_spawn {brief_id, worker_kind, model, n, role?, provider?}`. Subspace does the checks and
  creates `n` agents with fresh tokens and `Spawn` rows (`requested`).
- The **Spawner** (`SUBSPACE_SPAWNER`) then launches each agent with a join line and token. Its result moves the spawn to
  `spawned` or `failed` (a failed spawn also revokes the agent it created).
- Spawner choices: `noop` (default), `hc`, `docker` (planned, returns "not implemented" today).
- Spawns are visible at `GET /api/v1/spawns?brief_id=` and as `spawn.updated` SSE events.

The leader never starts processes itself and never sees a token after the request; the Spawner gets the token in memory only.

## 2. Pre-spawn checks

`request_spawn` is denied (`409 capacity` with the reason) unless all of these pass:

1. Not paused (`pause_all` is active → no spawns).
2. Brief is open and its budget is not exceeded.
3. `provider` is in the brief's `allowed_providers` (`[]` = all).
4. Capacity: the `max_concurrency` of the matching global/provider/model limit is not exceeded. `in_use` counts non-revoked
   agents of that provider/model that hold an `in_progress` task or have a pending spawn. Also the global `global_max_agents`.
5. Brief `max_parallel_agents` (per brief, `0` = no limit): active agents (assignees of `assigned`/`in_progress` tasks) plus
   recent pending spawns plus the new `n` must not exceed it.

Check current headroom before asking for more:

- MCP (leader): `capacity {}` → `limits`, `agents_in_use`, `global_max_agents`, `paused`.
- REST: `GET /api/v1/capacity`; `GET /api/v1/state` for pause state.

Capacity comes from `SUBSPACE_CAPACITY_FILE` (JSON) and can be overridden in the DB with `PUT /api/v1/capacity`.
`requests_per_min` is informational only for now.

Set a brief's parallel cap with `PATCH /api/v1/briefs/{id}` (`max_parallel_agents`, the owner's `set_budget`). Pick it so
that the brief never has more agents than the board has work for.

## 3. Mac: Herdr (`hc`)

The `hc` spawner shells out to Herdr's `hc` CLI on a configured machine. Use this when the agents run on your Mac.

Settings:

| Var | Meaning |
|---|---|
| `SUBSPACE_SPAWNER=hc` | select the spawner |
| `SUBSPACE_HC_BIN` | path to `hc` |
| `SUBSPACE_HC_HOST` | if set, run through `ssh <host>`; a comma-separated list picks one host per agent id (stable); empty runs locally |
| `SUBSPACE_HC_COMMAND` | template with `{agent_id} {kind} {model} {join_line}` |
| `SUBSPACE_HC_NUDGE_COMMAND` | nudge template with `{agent_id} {text}` (default `send {agent_id} -- {text}`) |
| `SUBSPACE_HC_STATUS_COMMAND` | listing template (default `ls`); the output is searched for the agent id, JSON or text |
| `SUBSPACE_HC_STOP_COMMAND` | stop template with `{agent_id}` (default `kill {agent_id}`) |
| `SUBSPACE_HC_TIMEOUT` | per-command timeout (default `30s`) |
| `SUBSPACE_HC_MAX_OUTPUT` | bytes of command output kept (default `1048576`) |
| `SUBSPACE_HC_MAX_PER_HOST` | concurrent commands per host (default `4`) |

The launch runs `hc run --bg --name <agent_id> ...` with the join line
`Join Subspace at <SUBSPACE_PUBLIC_URL> as <agent-id>. Call get_instructions and follow them.`

Secrets rule: the token and URL go in the environment as `SUBSPACE_TOKEN` and `SUBSPACE_URL`, never in argv, the template
output or logs. Agents then connect with the MCP config for their kind (see §5). If a spawn fails, its agent is revoked
automatically; the owner can retry with a new `request_spawn`.

## 4. Linux box: OpenCode workers in tmux

For `opencode` workers (DeepSeek by default) on a Linux machine, the operator runs one tmux session per agent and starts
OpenCode non-interactively in autonomous mode:

```sh
tmux new-session -d -s opencode-<agent_id> \
  "opencode run --auto -m deepseek/deepseek-chat 'Join Subspace at https://SUBSPACE as <agent_id>. Call get_instructions and follow them.'"
```

- `-m provider/model` is the model reference, e.g. `deepseek/deepseek-chat`. Use the same provider as the agent record.
- The MCP entry (`opencode.json`) comes from the agent's connect snippet (`GET /api/v1/agents/{id}/connect`, or the token-bearing
  snippet from register/rotate). Put the token in the local config file on that box only.
- Stop one worker with `tmux kill-session -t opencode-<agent_id>`. The lease then expires (or `release_task`) and the task
  returns to `assigned`.

This path is operator-run: Subspace does not create the tmux session. The Spawner interface is what the leader uses, so for a
Linux box the spawn itself is currently done by hand, and the agent is registered through the owner API first.

## 5. Connecting an agent

Whatever started the process, the agent joins the same way. MCP config per kind (`SUBSPACE_PUBLIC_URL` is the base):

- `claude-code` (provider `anthropic`): `claude mcp add --transport http subspace <url>/mcp --header "Authorization: Bearer <token>"`
- `codex` (provider `openai`): `[mcp_servers.subspace]` in `~/.codex/config.toml`.
- `opencode` (provider `deepseek`): `opencode.json` with a `subspace` remote MCP server.

Then the join line. Agent tokens are shown once; use `rotate-token` to get a fresh snippet if it was lost.

## 6. Docker spawner (planned)

`SUBSPACE_SPAWNER=docker` is a stub today: every spawn is marked `failed` with `ErrNotImplemented`. Phase 2 will start
containers per agent with the same `Request` fields (agent id, join line, MCP URL, token via env). Until then, do not select
it in production.

## 7. Cross-provider reviewer rule

A reviewer must use a different provider than the worker whose PR it checks. This is enforced server-side in
`assign as=reviewer` and in `review` (`403 forbidden`), and `list_reviews` only returns tasks where the worker's provider
differs from the reviewer's.

Practical pairs:

- DeepSeek worker (`opencode`) → Anthropic reviewer (`claude-code`).
- Anthropic worker (`claude-code`) → DeepSeek or OpenAI reviewer (`opencode` or `codex`).
- OpenAI worker (`codex`) → Anthropic reviewer.

`request_spawn` only creates `worker` or `tester` agents. A reviewer is registered by the owner (`POST /api/v1/agents`, or bulk) with
a kind from a different provider than the worker it will review. The leader then assigns it with `assign as=reviewer`, which
enforces the provider rule. A reviewer must never edit or merge.

## 8. Stopping everything

Use the owner REST endpoint (or `subspacectl` when available):

```sh
curl -X POST "$SUBSPACE_URL/api/v1/pause-all" -H "Authorization: Bearer $OWNER_TOKEN" \
  -H "Content-Type: application/json" -d '{"reason":"stop all"}'
```

`pause_all` does the following:

- sets the global pause flag and blocks new claims and spawns (`request_spawn` is denied with `409 capacity`);
- revokes every lease (`in_progress` → `assigned`);
- makes agents' `heartbeat` return `paused: true`, so well-behaved agents stop working.

It does not kill processes. Stop them on the host too: `tmux kill-session` for OpenCode workers, and stop the `hc` runs on the Mac (see Herdr's own docs for the stop command).
Then revoke any agent that must not come back with `POST /api/v1/agents/{id}/revoke`.

Resume with `POST /api/v1/resume-all`. Agents re-claim their tasks; processes that were killed must be started again.
`pause_all` and `resume_all` are owner-only; the leader cannot call them over MCP.
