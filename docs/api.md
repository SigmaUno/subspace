# Subspace API contract (v1)

This is the contract between the Go server and its clients (Next.js UI, `subspacectl`, agents). Server code must match it; if
the server changes, this file changes in the same PR.

- Base URL: `SUBSPACE_PUBLIC_URL` (e.g. `https://subspace.example.com`). Local default `http://localhost:8080`.
- All bodies are JSON (`Content-Type: application/json`), UTF-8. Timestamps are RFC 3339 UTC (`2026-11-20T21:04:05Z`); absent = `null`.
- IDs are short strings: briefs `B-<n>`, plans `P-<n>`, tasks `T-<n>`, questions `Q-<n>`, spawns `S-<n>`. Messages and audit events have integer ids. Agent ids are chosen by the owner (`[a-z0-9][a-z0-9_-]{0,62}`).
- Heartbeat data is held in memory and flushed to the DB every ~2s. REST reads overlay the in-memory values, so `last_heartbeat_at` / `lease_expires_at` are always current.

## 1. Auth

| Surface | Credential | Notes |
|---|---|---|
| `/api/v1/*` | `Authorization: Bearer <owner token>` | Owner token from env `SUBSPACE_OWNER_TOKEN` or `SUBSPACE_OWNER_TOKEN_FILE`. Agent tokens are rejected here (403). |
| `/mcp` | `Authorization: Bearer <agent token>` | One token per agent. The owner token is rejected here (403): owner actions are never reachable through MCP. |
| `/healthz` | none | `{"status":"ok","version":"...","paused":false}` |

Agent tokens are 32 random bytes, base64url, prefixed `ssp_`. Only SHA-256 hashes are stored; the plaintext is returned exactly once (register / rotate). Comparison is constant-time.

Errors (REST): HTTP status + body
```json
{"error": {"code": "not_found", "message": "task T-9 not found"}}
```
Codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403, `not_found` 404, `conflict` 409 (invalid transition / duplicate), `paused` 423, `capacity` 409, `rate_limited` 429, `internal` 500.

Missing/invalid bearer → 401 with `WWW-Authenticate: Bearer`.

In the Compose deployment, Caddy routes `/api/v1/*` (including SSE) through Next.js. The proxy adds the bearer from the browser's httpOnly owner cookie. It also accepts an explicit bearer from CLI/API clients without a session cookie, so `SUBSPACE_URL` can point to the public Caddy URL. Next validates both with Go’s owner state endpoint before forwarding requests to protected handlers. Go also authenticates each protected request.

## 2. Objects

```jsonc
// Brief
{"id":"B-1","title":"...","body":"markdown brief","repos":["SigmaUno/foo"],"status":"open",   // open|closed
 "budget":{"max_cost_eur":50.0,"max_parallel_agents":10},
 "allowed_providers":["anthropic","openai","deepseek"],   // [] = all
 "cost_used":3.2,"plan_status":"approved",                 // none|proposed|approved|rejected
 "created_at":"..."}

// Task
{"id":"T-3","brief_id":"B-1","plan_id":"P-1","title":"...","spec":"...","acceptance_criteria":["..."],
 "repo":"SigmaUno/foo","branch":"sub/t-3-add-login","role":"worker",      // worker|tester
 "assignee":"claude-1","reviewer":null,"depends_on":["T-1"],
 "status":"in_progress",
 "progress_pct":40,"last_heartbeat_at":"...","lease_expires_at":"...","pr_url":null,
 "budget":{"max_minutes":120,"max_cost_eur":5.0},"cost_used":0.4,"minutes_used":12,
 "review_rounds":0,"tests":null,                       // or {"passed":10,"failed":1,"reported_at":"..."}
 "created_at":"...","updated_at":"..."}
// status: backlog|ready|assigned|in_progress|submitted|in_review|changes_requested|approved|done|blocked|needs_owner|cancelled
// Task.blocked_reason (string|null) is set when status is blocked/needs_owner.

// Plan
{"id":"P-1","brief_id":"B-1","status":"proposed",   // proposed|approved|rejected|superseded
 "proposed_by":"rex","note":null,"tasks":[/* Task, status backlog */],"created_at":"...","decided_at":null}

// Agent
{"id":"claude-1","role":"worker",                  // leader|worker|tester|reviewer
 "kind":"claude-code",                             // claude-code|codex|opencode|other (anything: leader/reviewer agents)
 "model":"claude-sonnet-5-5","provider":"anthropic",
 "status":"active",                                // active|paused|revoked
 "pause_reason":null,"current_task_id":"T-3","assigned_count":2,
 "last_seen_at":"...","created_at":"...","spawned":false}

// Message (thread entry)
{"id":42,"thread_kind":"task","thread_id":"T-3","author_id":"claude-1","author_role":"worker",
 "kind":"message","body":"...","created_at":"..."}
// thread_kind: agent|task|brief ; kind: message|status|question|answer|note|system

// Question
{"id":"Q-1","brief_id":"B-1","task_id":"T-3","agent_id":"claude-1","question":"...","options":["a","b"],
 "status":"open","answer":null,"answered_at":null,"created_at":"..."}            // open|answered

// AuditEvent
{"id":7,"ts":"...","agent_id":"claude-1","role":"worker","tool":"claim_task","task_id":"T-3",
 "before":{"status":"assigned"},"after":{"status":"in_progress"},"note":null}

// CapacityLimit
{"provider":"anthropic","model":"*","max_concurrency":20,"requests_per_min":600,"in_use":4,"available":16}

// Spawn
{"id":"S-1","brief_id":"B-1","agent_id":"claude-b1-1","kind":"claude-code","model":"...","provider":"anthropic",
 "status":"spawned","spawner":"hc","error":null,"requested_by":"rex","created_at":"..."}   // requested|spawned|failed

// Connect info (owner REST; shown in UI)
{"agent_id":"claude-1","join_line":"Join Subspace at https://s.example.com as claude-1. Call get_instructions and follow them.",
 "mcp_url":"https://s.example.com/mcp",
 "snippets":{"claude_code":"claude mcp add --transport http subspace https://s.example.com/mcp --header \"Authorization: Bearer <token>\"",
             "codex_toml":"[mcp_servers.subspace]\nurl = \"https://s.example.com/mcp\"\nhttp_headers = { Authorization = \"Bearer <token>\" }\n",
             "opencode_json":"{\"mcp\":{\"subspace\":{\"type\":\"remote\",\"url\":\"https://s.example.com/mcp\",\"headers\":{\"Authorization\":\"Bearer <token>\"}}}}"},
 "token":"ssp_..."}   // token present only in register/rotate responses; snippets then contain it instead of <token>
```

## 3. REST `/api/v1` (owner token)

List endpoints return `{"<plural>": [...]}`. `limit` default 100, max 1000.

### Control & state
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/state` | → `{"paused":false,"pause_reason":null,"counts":{"agents":12,"agents_active":10,"tasks":{"ready":3,"in_progress":5,...},"open_questions":2,"proposed_plans":1},"version":"..."}` |
| POST | `/api/v1/pause-all` | `{"reason":"..."}` → `{"paused":true}`. Revokes all leases (in_progress → assigned), blocks claims. |
| POST | `/api/v1/resume-all` | `{}` → `{"paused":false}` |
| GET/PUT | `/api/v1/charter` | `{"text":"..."}` ↔ `{"text":"...","updated_at":"..."}`. Charter text is appended to the built-in instructions returned by `get_instructions`. |
| POST | `/api/v1/backup` | `{}` → `{"path":"/data/subspace/backups/subspace-20261120T210405Z.db","bytes":12345}` (`VACUUM INTO`). |

### Briefs
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/briefs` | `{"title","body","repos":[],"max_cost_eur":50,"max_parallel_agents":10,"allowed_providers":[],"cross_review":true}` → Brief (201). `cross_review` defaults to `true`. |
| GET | `/api/v1/briefs?status=` | → `{"briefs":[Brief]}` |
| GET | `/api/v1/briefs/{id}` | → Brief |
| PATCH | `/api/v1/briefs/{id}` | any of `max_cost_eur, max_parallel_agents, allowed_providers, cross_review, status` → Brief (set_budget) |
| GET | `/api/v1/briefs/{id}/board` | → `{"brief":Brief,"columns":{"backlog":[Task],"ready":[...],...all 12 statuses...}}` |
| GET | `/api/v1/briefs/{id}/architecture` | → `{"brief_id","nodes":[{"id":"C-1","brief_id","name","description","paths":[],"depends_on":["C-2"],"owners":[{"agent_id","role","agent_status","task_ids":[]}],"status":{"counts":{status:n},"total","progress_avg","blocked","stale","last_activity"}}],"edges":[{"from":"C-1","to":"C-2"}]}` |
| PUT | `/api/v1/briefs/{id}/components` | `{"components":[{"name","description","paths":[],"depends_on":["<component name>"]}]}` → `{"components":[Component]}`. Replaces the plan; ids `C-<n>` follow list order. 400 on an empty plan, duplicate names, unknown/self/cyclic dependencies or paths that are absolute or escape the repository. |

### Plans
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/plans?status=proposed&brief_id=` | → `{"plans":[Plan]}` |
| GET | `/api/v1/plans/{id}` | → Plan |
| POST | `/api/v1/plans/{id}/approve` | `{"note":""}` → Plan. Tasks `backlog → ready`. 409 if not `proposed`. |
| POST | `/api/v1/plans/{id}/reject` | `{"reason":"..."}` → Plan. Tasks → `cancelled`; reason posted to the brief thread. |

### Tasks
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/tasks?brief_id=&status=&assignee=&role=` | → `{"tasks":[Task]}` |
| GET | `/api/v1/tasks/{id}` | → Task |
| PATCH | `/api/v1/tasks/{id}` | `{"budget":{"max_minutes","max_cost_eur"}}` → Task (set_budget); unblocks a budget-blocked task if now within budget |
| POST | `/api/v1/tasks/{id}/done` | `{"note":"merged in #123"}` → Task. Only from `approved` (or `needs_owner`). Sets `done`. |
| POST | `/api/v1/tasks/{id}/cancel` | `{"reason":""}` → Task |
| POST | `/api/v1/tasks/{id}/reopen` | `{"status":"ready|assigned","note":""}` → Task. From `needs_owner`/`blocked`/`cancelled`. Resets `review_rounds` if `needs_owner`. |

### Agents
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/agents?role=&status=` | → `{"agents":[Agent]}` |
| GET | `/api/v1/agents/{id}` | → Agent |
| POST | `/api/v1/agents` | `{"id","role","kind","model","provider"}` → `{"agent":Agent,"connect":Connect}` (201; `connect.token` shown once). `provider` defaults from kind (claude-code→anthropic, codex→openai, opencode→deepseek). |
| POST | `/api/v1/agents/bulk` | `{"prefix":"claude","count":50,"role":"worker","kind":"claude-code","model":"...","provider":""}` → `{"agents":[{"agent":Agent,"connect":Connect}]}`. Ids `<prefix>-001`…; max count 1000; atomic. |
| GET | `/api/v1/agents/{id}/connect` | → Connect without token (snippets keep `<token>`). |
| POST | `/api/v1/agents/{id}/rotate-token` | `{}` → `{"agent":Agent,"connect":Connect}` (new token once; old one dead) |
| POST | `/api/v1/agents/{id}/revoke` | `{"reason":""}` → Agent. Token dead immediately, leases released. |
| POST | `/api/v1/agents/{id}/resume` | `{}` → Agent (clears loop-protection pause) |

### Questions
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/questions?status=open` | → `{"questions":[Question]}` |
| POST | `/api/v1/questions/{id}/answer` | `{"answer":"..."}` → Question. Posts an `answer` message to the task and agent threads. |

### Threads
Thread key `{kind}/{id}` with kind `agent|task|brief`.
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/threads/{kind}/{id}/messages?since=<message id>&limit=` | → `{"messages":[Message]}` ascending by id; `since` returns ids > since. |
| POST | `/api/v1/threads/{kind}/{id}/messages` | `{"body":"..."}` → Message (author_role `owner`, kind `message`) |

### Capacity & spawns
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/capacity` | → `{"global_max_agents":300,"limits":[CapacityLimit],"agents_in_use":12}` |
| PUT | `/api/v1/capacity` | `{"global_max_agents":300,"limits":[{"provider","model","max_concurrency","requests_per_min"}]}` → same as GET (set_capacity; persisted in DB, overrides file) |
| GET | `/api/v1/spawns?brief_id=` | → `{"spawns":[Spawn]}` |

### Audit
`GET /api/v1/audit?agent_id=&task_id=&tool=&since=<id>&limit=` → `{"events":[AuditEvent]}` ascending; append-only, no write endpoint.

## 4. SSE: `GET /api/v1/events`

Owner token header. (Browsers: proxy via the Next.js server; `EventSource` cannot set headers.)
Query `topics=` comma list, default `all`: `all`, `board` (every task change), `brief:<id>`, `task:<id>`, `agents`, `agent:<id>`, `thread:<kind>:<id>`, `questions`, `plans`, `control`.

Response `text/event-stream`, a `: ping` comment every 15s. Each event:
```
id: 1234
event: task.updated
data: {"type":"task.updated","ts":"2026-11-20T21:04:05Z","data":{...}}
```
`id` is a per-server monotonic sequence. Replay is not guaranteed: on reconnect, refetch state, then follow the stream. First event is always `hello` (`{"seq":1234}`).

| type | data | topics |
|---|---|---|
| `hello` | `{"seq":n}` | – |
| `task.created` / `task.updated` | Task (heartbeats are coalesced: at most one `task.updated` per task per 2s) | board, brief:*, task:* |
| `agent.updated` | Agent | agents, agent:* |
| `message.created` | Message | thread:*, plus board for kind `question` |
| `question.created` / `question.answered` | Question | questions |
| `plan.proposed` / `plan.approved` / `plan.rejected` | Plan | plans, brief:* |
| `spawn.updated` | Spawn | agents, brief:* |
| `control.paused` / `control.resumed` | `{"reason":"..."}` | control |
| `capacity.changed` | capacity object (as GET /capacity) | control |
| `lagged` | `{"dropped":n}` — slow-client coalescing happened; refetch | all |

Slow subscribers have a bounded buffer; when it overflows, oldest events are dropped and a `lagged` event is delivered.

## 5. MCP: `POST /mcp` (agent token)

Streamable HTTP, JSON-RPC 2.0, protocol version `2025-06-18` (older `2025-03-26` accepted). One JSON-RPC message per POST (batching unsupported). `Accept` should include `application/json`; responses are `application/json` (no SSE streaming). Notifications/responses → `202`. `GET /mcp` → 405. `DELETE /mcp` with `Mcp-Session-Id` ends the session.

`initialize` returns `Mcp-Session-Id` (bound to the agent). The header is optional afterwards, but if sent and unknown/expired → 404 (client re-initializes). Methods: `initialize`, `notifications/initialized`, `ping`, `tools/list`, `tools/call`.

`tools/list` returns only tools the caller's role may call. `tools/call` re-checks; calling a tool outside the role → JSON-RPC error `-32601 method not found` style `tool not available for role`.

`tools/call` result: `{"content":[{"type":"text","text":"<json>"}],"structuredContent":{...},"isError":false}`. Tool errors: `isError:true` and `structuredContent:{"error":{"code","message"}}` (codes as REST plus `agent_paused`, `lease_expired`, `not_assigned`, `budget_exceeded`).

`get_instructions` content is stable (see SPEC §6) and ends with the owner charter.

### Tool permissions (✓ = in `tools/list` for the role)

| Tool | leader | worker | tester | reviewer |
|---|:-:|:-:|:-:|:-:|
| get_instructions, whoami, list_my_tasks, get_task, heartbeat, update_status, add_note, ask_owner, post_message, read_thread | ✓ | ✓ | ✓ | ✓ |
| claim_task, release_task, submit | | ✓ | ✓ | |
| report_tests | | | ✓ | |
| list_reviews, review | | | | ✓ |
| get_brief, list_briefs, propose_plan, create_task, update_task, assign, reassign, board, list_agents, capacity, request_spawn | ✓ | | | |

Owner-only actions (approve_plan, reject_plan, answer_question, set_budget, register_agent(s), revoke_agent, pause_all, resume_all, edit_charter, create_brief, set_capacity, mark done) are REST/CLI only.

### Tool input schemas (JSON Schema, abbreviated)

All tools return an object; `task` = Task object above.

**Common**
- `get_instructions {}` → `{"instructions":"<text>","role":"worker","agent_id":"..."}`
- `whoami {}` → `{"agent":Agent,"paused":false,"server_time":"..."}`
- `list_my_tasks {"status?":["in_progress",...]}` → `{"tasks":[Task]}` (tasks where I am assignee or reviewer)
- `get_task {"task_id"}` → `{"task":Task}` (own/assigned tasks; leader: any)
- `heartbeat {"task_id?","note?"}` → `{"ok":true,"paused":false,"lease_expires_at":"..."|null}`. Extends leases of my in_progress tasks (or only `task_id`). Cheap (in-memory, batched).
- `update_status {"task_id","status":"in_progress|blocked","progress_pct?":0-100,"note?","cost_eur?":number}` → `{"task":Task}`. `cost_eur` is cumulative cost spent. Exceeding task/brief budget forces `blocked`. Cannot set submitted/approved/done.
- `add_note {"task_id?","body"}` → `{"message":Message}` (task thread, or own agent thread if no task)
- `ask_owner {"task_id?","question","options?":["..."]}` → `{"question":Question}`; answer arrives in the task/agent thread (`read_thread`).
- `post_message {"thread":"task:T-3","body"}` → `{"message":Message}`. `thread` is `agent:<id>` (or `agent:me`), `task:<id>`, `brief:<id>`. Scope: own agent thread; task threads where I am assignee/reviewer; leader additionally every task/brief thread.
- `read_thread {"thread","since?":int,"limit?":int}` → `{"messages":[Message]}`

**Worker/tester**
- `claim_task {"task_id"}` → `{"task":Task,"lease_expires_at":"..."}`. Requires: assigned to me, status `assigned` or `changes_requested`, dependencies `done`, not paused, within brief max_parallel_agents/budget.
- `release_task {"task_id","reason?"}` → `{"task":Task}` (→ `assigned`)
- `submit {"task_id","pr_url","summary"}` → `{"task":Task}` (→ `submitted`, or → `approved` when the brief has `cross_review:false`; releases lease; `pr_url` required for worker, optional for tester)

**Tester**
- `report_tests {"task_id","passed":int,"failed":int,"log_excerpt?"}` → `{"task":Task}`

**Reviewer**
- `list_reviews {}` → `{"tasks":[Task]}` — `submitted`/`in_review` tasks where `reviewer` is me or unset, and the worker's provider differs from mine.
- `review {"task_id","verdict":"approve|changes_requested","comments"}` → `{"task":Task}`. approve → `approved`; changes_requested → `changes_requested` (`review_rounds`+1); at 3 rounds → `needs_owner`.

**Leader**
- `get_brief {"brief_id"}` / `list_briefs {"status?"}` → Brief(s)
- `propose_plan {"brief_id","note?","tasks":[{"ref?","title","spec","acceptance_criteria":["..."],"repo","depends_on?":["<ref or T-id>"],"role?":"worker|tester","budget?":{"max_minutes","max_cost_eur"}}]}` → `{"plan":Plan}`. `ref` lets tasks depend on each other inside the plan. Supersedes any earlier proposed plan.
- `create_task {"brief_id", ...task fields}` → `{"task":Task}` (status `ready` if the brief has an approved plan, else `backlog`)
- `update_task {"task_id","title?","spec?","acceptance_criteria?","depends_on?","budget?","status?":"blocked|ready|cancelled"}` → `{"task":Task}` (`ready` on a `blocked` task unblocks it: → `assigned` if it has an assignee, else `ready`; `backlog` tasks only become ready through owner plan approval)
- `assign {"task_id","agent_id","as?":"assignee|reviewer"}` → `{"task":Task}` (`ready → assigned`; reviewer on `submitted` → `in_review`). Checks agent role, `active`, provider allowed by brief; reviewer provider ≠ worker provider.
- `reassign {"task_id","agent_id","reason?"}` → `{"task":Task}` (releases lease; → `assigned`)
- `board {"brief_id?"}` → `{"columns":{status:[Task]}}`
- `list_agents {"role?","status?"}` → `{"agents":[Agent]}`
- `capacity {}` → `{"global_max_agents":n,"limits":[CapacityLimit],"agents_in_use":n,"paused":bool}`
- `request_spawn {"brief_id","worker_kind":"claude-code|codex|opencode","model","n":1-50,"role?":"worker|tester","provider?"}` → `{"spawns":[Spawn]}`; 409 `capacity` with the reason when capacity / max_parallel_agents / provider allow-list / budget / pause denies it.

### Threads scoping summary
Agent thread `agent:<id>`: leader messages to the agent, owner replies, answers, system notices. Task thread `task:<id>`: status changes (auto), notes, questions/answers, reviews, tests. Brief thread `brief:<id>`: plan decisions, leader notes.

## 6. Swarm overview extensions

| Method | Path | Response or body |
|---|---|---|
| GET | `/api/v1/decisions` | `{"decisions":[Decision],"count":n}`, ordered by impact descending, creation time ascending |
| GET | `/api/v1/decisions/next` | `{"decision":Decision|null,"count":n}` |
| POST | `/api/v1/decisions/{id}` | `{"action":"approve|reject|answer|unblock","answer":"...","note":"..."}` |
| GET | `/api/v1/briefs/{id}/architecture` | `{"components":[Component]}` |
| POST | `/api/v1/messages` | `{"to":"agent:ID|component:ID|brief:ID|leader","body":"..."}` → `{"conversation_id":"ID"}` |
| GET | `/api/v1/conversations/{id}` | `{"id":"ID","messages":[Message]}` |
| GET | `/api/v1/hillclimb/jobs?brief_id={id}` | `{"jobs":[Job]}` |
| GET | `/api/v1/hillclimb/jobs/{id}/series` | `{"job":Job,"attempts":[Attempt]}`, oldest attempt first |

- Decision fields are `id`, `kind` (`plan|question|merge|unblock`), `title`, `body`, `brief_id`, `task_id`, `agent_id`, `pr_url`, `options`, `impact` and `created_at`. Approving a merge decision records owner approval.
- Component fields are `id`, `name`, `description`, `depends_on`, `paths`, `owner_agent_ids`, `task_ids`, and optional `status_counts`, `progress_pct`, `stale_count` and `last_activity_at`. Tasks may carry `component_id`.
- `GET /api/v1/agents?include=last_message` may include an agent's latest `Message` across the agent thread and current task thread.
- Job fields are `id`, `title`, `metric`, `direction` (`min|max`), `status`, and optional `target` and `stop_reason`. Attempt fields are `id`, `value` (null if failed or pending), `branch`, `branch_url` and `created_at`.
- The owner overview accepts `decision.created`, `decision.updated`, `decision.resolved`, `architecture.updated`, `conversation.message`, `hillclimb.updated` and `hillclimb.attempt` on the SSE stream. Existing plan, question, task and message events also refresh their associated panels.
