# Subspace API contract (v1)

This is the contract between the Go server and its clients (Next.js UI, `subspacectl`, agents). Server code must match it; if
the server changes, this file changes in the same PR.

- Base URL: `SUBSPACE_PUBLIC_URL` (e.g. `https://subspace.example.com`). Local default `http://localhost:8080`.
- All bodies are JSON (`Content-Type: application/json`), UTF-8. Timestamps are RFC 3339 UTC (`2026-11-20T21:04:05Z`); absent = `null`.
- IDs are short strings: briefs `B-<n>`, plans `P-<n>`, tasks `T-<n>`, questions `Q-<n>`, spawns `S-<n>`, components `C-<n>`, artifacts `A-<n>`, hillclimb jobs `H-<n>`, conversations `C-<16 hex>`. Messages and audit events have integer ids. Agent ids are chosen by the owner (`[a-z0-9][a-z0-9_-]{0,62}`).
- Heartbeat data is held in memory and flushed to the DB every ~2s. REST reads overlay the in-memory values, so `last_heartbeat_at` / `lease_expires_at` are always current.

## 1. Auth

| Surface | Credential | Notes |
|---|---|---|
| `/api/v1/*` | `Authorization: Bearer <owner token>` | Owner token from env `SUBSPACE_OWNER_TOKEN` or `SUBSPACE_OWNER_TOKEN_FILE`. Agent tokens are rejected here (403). |
| `/mcp` | `Authorization: Bearer <agent token>` | One token per agent. The owner token is rejected here (403): owner actions are never reachable through MCP. |
| `/healthz` | none | `{"status":"ok","version":"...","paused":false}` |
| `POST /hooks/github`, `POST /hooks/forgejo` | HMAC signature (`X-Hub-Signature-256`; `X-Forgejo-Signature` / `X-Gitea-Signature`) | Mounted only when `SUBSPACE_WEBHOOK_SECRET_GITHUB` / `SUBSPACE_WEBHOOK_SECRET_FORGEJO` is set. Body ≤ 1 MiB, `*-Delivery` id required, replayed delivery ids → 202 without effect. |

Agent tokens are 32 random bytes, base64url, prefixed `ssp_`. Only SHA-256 hashes are stored; the plaintext is returned exactly once (register / rotate). Comparison is constant-time.

Errors (REST): HTTP status + body
```json
{"error": {"code": "not_found", "message": "task T-9 not found"}}
```
Codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403, `not_found` 404, `conflict` 409 (invalid transition / duplicate), `paused` 423, `capacity` 409, `rate_limited` 429, `internal` 500, plus `too_large` 413 and `quota_exceeded` 507 (artifact uploads). Unknown `/api/v1/` paths return `not_found` with message `no such endpoint`.

Missing/invalid bearer → 401 with `WWW-Authenticate: Bearer`.

In the Compose deployment, Caddy routes `/api/v1/*` (including SSE) through Next.js. The proxy adds the bearer from the browser's httpOnly owner cookie. It also accepts an explicit bearer from CLI/API clients without a session cookie, so `SUBSPACE_URL` can point to the public Caddy URL. Next validates both with Go’s owner state endpoint before forwarding requests to protected handlers. Go also authenticates each protected request.

## 2. Objects

```jsonc
// Brief
{"id":"B-1","title":"...","body":"markdown brief","repos":["SigmaUno/foo"],"status":"open",   // open|closed
 "budget":{"max_cost_eur":50.0,"max_parallel_agents":10},
 "allowed_providers":["anthropic","openai","deepseek"],   // [] = all
 "cross_review":true,"auto_done_on_merge":false,
 "cost_used":3.2,"plan_status":"approved",                 // none|proposed|approved|rejected
 "created_at":"..."}

// Task
{"id":"T-3","brief_id":"B-1","plan_id":"P-1","title":"...","spec":"...","acceptance_criteria":["..."],
 "repo":"SigmaUno/foo","branch":"sub/t-3-add-login","role":"worker",      // worker|tester
 "assignee":"claude-1","reviewer":null,"depends_on":["T-1"],
 "status":"in_progress",
 "progress_pct":40,"last_heartbeat_at":"...","lease_expires_at":"...","pr_url":null,
 "budget":{"max_minutes":120,"max_cost_eur":5.0},"cost_used":0.4,"minutes_used":12,
 "review_rounds":0,"priority":0,"component_id":"","tests":null,                       // or {"passed":10,"failed":1,"reported_at":"..."}
 "blocked_reason":null,                                // string when status is blocked or needs_owner
 "created_at":"...","updated_at":"..."}
// status: backlog|ready|assigned|in_progress|submitted|in_review|changes_requested|approved|done|blocked|needs_owner|cancelled

// Plan
{"id":"P-1","brief_id":"B-1","status":"proposed",   // proposed|approved|rejected|superseded
 "proposed_by":"rex","note":null,"tasks":[/* Task, status backlog */],
 "warnings":[{"severity":"warning","rule":"no_tester","task":"a","message":"..."}],   // lint warnings, [] when none; task omitted for plan-wide findings
 "created_at":"...","decided_at":null}

// Agent
{"id":"claude-1","role":"worker",                  // leader|worker|tester|reviewer
 "kind":"claude-code",                             // claude-code|codex|opencode|other (anything: leader/reviewer agents)
 "model":"claude-sonnet-5-5","provider":"anthropic",
 "status":"active",                                // active|paused|revoked
 "pause_reason":null,"current_task_id":"T-3","assigned_count":2,
 "last_seen_at":"...","created_at":"...","spawned":false}

// Message
{"id":42,"thread_kind":"task","thread_id":"T-3","author_id":"claude-1","author_role":"worker",
 "kind":"message","body":"...","created_at":"..."}
// thread entry. thread_kind: agent|task|brief ; kind: message|status|question|answer|note|system

// Question
{"id":"Q-1","brief_id":"B-1","task_id":"T-3","agent_id":"claude-1","question":"...","options":["a","b"],
 "status":"open","answer":null,"answered_at":null,"created_at":"..."}            // open|answered

// AuditEvent
{"id":7,"ts":"...","agent_id":"claude-1","role":"worker","tool":"claim_task","task_id":"T-3",
 "before":{"status":"assigned"},"after":{"status":"in_progress"},"note":null,"hash":"<sha256 hex>"}

// CapacityLimit
{"provider":"anthropic","model":"*","max_concurrency":20,"requests_per_min":600,"in_use":4,"available":16}

// Spawn
{"id":"S-1","brief_id":"B-1","agent_id":"claude-b1-1","kind":"claude-code","model":"...","provider":"anthropic",
 "status":"spawned","spawner":"hc","error":null,"requested_by":"rex","created_at":"..."}   // requested|spawned|failed|stopped

// SpawnLifecycle: a Spawn plus these fields (GET /api/v1/spawns, POST /api/v1/spawns/{id}/stop)
{"lifecycle":"idle","connected_at":"...","idle_since":"...","stopped_at":null,"stop_reason":null,
 "process":"running","process_checked_at":"..."}
// lifecycle: pending|connecting|active|idle|stopped|failed ; process: running|exited|unknown
// stop_reason: connect_timeout|idle|brief_closed|paused|agent_revoked|exited|owner

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
| POST | `/api/v1/backup` | `{}` → `{"path":"/data/subspace/backups/subspace-20261120T210405Z.db","bytes":12345}` (`VACUUM INTO` into `SUBSPACE_BACKUP_DIR`; 400 when no backup directory is configured). |

### Briefs
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/briefs` | `{"title","body","repos":[],"max_cost_eur":50,"max_parallel_agents":10,"allowed_providers":[],"cross_review":true}` → Brief (201). `cross_review` defaults to `true`. |
| GET | `/api/v1/briefs?status=` | → `{"briefs":[Brief]}` |
| GET | `/api/v1/briefs/{id}` | → Brief |
| PATCH | `/api/v1/briefs/{id}` | any of `max_cost_eur, max_parallel_agents, allowed_providers, cross_review, auto_done_on_merge, status` → Brief (set_budget) |
| GET | `/api/v1/briefs/{id}/board` | → `{"brief":Brief,"columns":{"backlog":[Task],"ready":[...],...all 12 statuses...}}`; tasks in each column and in task lists are ordered by `priority` descending, then id. |
| GET | `/api/v1/briefs/{id}/architecture` | → `{"brief_id","nodes":[{"id":"C-1","brief_id","name","description","paths":[],"depends_on":["C-2"],"owners":[{"agent_id","role","agent_status","task_ids":[]}],"status":{"counts":{status:n},"total","progress_avg","blocked","stale","last_activity"}}],"edges":[{"from":"C-1","to":"C-2"}]}` |
| PUT | `/api/v1/briefs/{id}/components` | `{"components":[{"name","description","paths":[],"depends_on":["<component name>"]}]}` → `{"components":[Component]}`. Replaces the plan; ids `C-<n>` follow list order. 400 on an empty plan, duplicate names, unknown/self/cyclic dependencies or paths that are absolute or escape the repository. |

### Plans
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/plans?status=proposed&brief_id=` | → `{"plans":[Plan]}` |
| GET | `/api/v1/plans/{id}` | → Plan |
| POST | `/api/v1/plans/{id}/approve` | `{"note":""}` → Plan. Tasks `backlog → ready`. 409 if not `proposed`. |
| POST | `/api/v1/plans/{id}/reject` | `{"reason":"..."}` → Plan. Tasks → `cancelled`; reason posted to the brief thread. |
| POST | `/api/v1/plans/{id}/tasks` | `{"title","spec","acceptance_criteria":[],"repo","role":"worker|tester","depends_on":["T-1"],"budget":{}}` → Plan (201). Adds a `backlog` task to a `proposed` plan. 409 if the plan is not `proposed`; 400 on invalid fields, a repo outside the brief, or an unknown, cancelled, foreign-brief or cyclic dependency. |
| DELETE | `/api/v1/plans/{id}/tasks/{taskId}` | → Plan. Drops a `backlog` task of a `proposed` plan: the task becomes `cancelled` and stays in `tasks`. 409 while a non-cancelled task still lists it in `depends_on` (edit those first, the message names them) or if the plan is not `proposed`; 404 if the task is not part of the plan. |

Plan edits post a system message to the brief thread and append an audit event (`add_plan_task`, `drop_plan_task`).

### Tasks
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/tasks?brief_id=&status=&assignee=&role=` | → `{"tasks":[Task]}` |
| GET | `/api/v1/tasks/{id}` | → Task |
| PATCH | `/api/v1/tasks/{id}` | any of `title, spec, acceptance_criteria, depends_on, repo, role, budget` → Task. A body with only `budget` is set_budget (any status; unblocks a budget-blocked task if now within budget). Any other field edits the task and requires status `backlog` or `ready` (409 otherwise). 400 on an empty body, empty title/spec, a repo outside the brief, a role other than `worker|tester`, a role change on an assigned task (409), or a dependency that is unknown, itself, cancelled, in another brief or forms a cycle. A title change regenerates `branch`. Effective edits post a system message to the brief thread and append an `edit_task` audit event with before/after of the changed fields. |
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
| GET | `/api/v1/capacity` | → `{"global_max_agents":300,"limits":[CapacityLimit],"agents_in_use":12,"paused":false}` |
| PUT | `/api/v1/capacity` | `{"global_max_agents":300,"limits":[{"provider","model","max_concurrency","requests_per_min"}]}` → same as GET (set_capacity; persisted in DB, overrides file) |
| GET | `/api/v1/spawns?brief_id=` | → `{"spawns":[SpawnLifecycle]}` |
| POST | `/api/v1/spawns/{id}/stop` | `{}` → SpawnLifecycle. Revokes the agent, asks the spawner to stop the process and marks the spawn `stopped` (`stop_reason` `owner`). 404 unknown spawn, 409 if the spawn is not `spawned`, 500 if the spawner fails to stop it (the agent stays revoked and the reaper retries). |

A reaper checks `spawned` spawns every `SUBSPACE_SPAWN_REAP_INTERVAL` (default `30s`). Stopping a spawn always revokes its agent, asks the spawner to stop the process and writes a `stop_spawn` audit event and a `spawn.updated` event.
- No heartbeat or tool call within `SUBSPACE_SPAWN_CONNECT_TIMEOUT` (default `5m`) of the spawn: the spawn becomes `failed` (`connect_timeout`).
- Connected but without assigned work (no task as assignee or reviewer in `assigned`, `in_progress`, `blocked`, `submitted`, `in_review`, `changes_requested` or `needs_owner`) for `SUBSPACE_SPAWN_IDLE_TTL` (default `15m`): `stopped` (`idle`).
- Brief closed: every spawn of the brief is `stopped` (`brief_closed`).
- `pause_all` lasting `SUBSPACE_SPAWN_PAUSE_GRACE` (default off): every spawn is `stopped` (`paused`).
- Agent revoked by other means: `stopped` (`agent_revoked`).
- On server start the spawner's status is asked for every `spawned` spawn: `exited` ones are `stopped` (`exited`), the others record `process`.

Durations use Go syntax; `0` disables a rule. The `noop` and `hc` spawners cannot stop or inspect processes: spawns are still marked stopped and their agents revoked, and `process` stays `unknown`.

### Audit
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/audit?agent_id=&task_id=&tool=&since=<id>&limit=` | → `{"events":[AuditEvent]}` ascending; append-only, no write endpoint. |

### Reports & architecture
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/briefs/{id}/report` | → Report. 404 for an unknown brief. |
| GET | `/api/v1/briefs/{id}/report.md` | → the same report as `text/markdown`. |

```jsonc
// Report
{"brief_id":"B-1","title":"...","generated_at":"...","total":4,"counts":{"done":3,"blocked":1},
 "tasks":[{"id":"T-1","title":"...","status":"done","assignee":"claude-1","provider":"anthropic","cost_eur":0.4,
           "created_at":"...","claimed_at":null,"submitted_at":null,"done_at":null,"claims":1,"review_rounds":0,
           "reviews":[{"at":"...","reviewer":"codex-r","verdict":"approve","wait_seconds":30}],
           "timeline":[{"at":"...","from":"ready","to":"assigned","by":"rex","tool":"assign","elapsed_seconds":0}],
           "time_in_status":{"assigned":12},"queue_seconds":0,"work_seconds":0,"review_seconds":0,
           "blocked_seconds":0,"active_seconds":0,"lead_seconds":0}],
 "cost":{"total_eur":0.4,"by_task":{"T-1":0.4},"by_agent":{"claude-1":0.4},"by_provider":{"anthropic":0.4}},
 "blocked_seconds":0,
 "throughput":{"done":3,"window_hours":2.5,"per_hour":1.2,"hourly":[{"hour":"...","done":2}]},
 "critical_path":{"task_ids":["T-1","T-2"],"seconds":3600},
 "outliers":[{"task_id":"T-3","metric":"work_seconds","value":9000,"threshold":3000}]}

// Component
{"id":"C-1","brief_id":"B-1","name":"api","description":"...","paths":["internal/api"],"depends_on":["C-2"]}
```

Components are held in memory per server process and are not linked to tasks yet, so architecture nodes always report empty `owners` and zero task counts. `PUT components` accepts only `name`, `description`, `paths` and `depends_on` (component names) per entry.

### Decisions
The owner inbox. Decisions are derived from open questions, proposed plans, `approved` tasks awaiting a merge, `blocked`/`needs_owner` tasks and stale agents (an active agent with a current task not seen for 10 minutes).

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/decisions?limit=` | → `{"decisions":[Decision]}` without snoozed ones, ordered by `priority` descending, then `created_at` ascending, then `id`. `limit` default 100, max 1000, 400 if not a positive integer. |
| GET | `/api/v1/decisions/next` | → `{"decision":Decision}`; `decision` is `null` when the inbox is empty. |
| POST | `/api/v1/decisions/{id}` | `{"action":"...", ...}` → `{"id":"q:Q-1","action":"answer","result":<result>,"next":Decision}`; `next` is `null` when nothing is left. 400 without `action`, 404 for an unknown id, 409 when the action is not in the decision's `actions`. |

```jsonc
// Decision
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent
 "title":"...","summary":"...","brief_id":"B-1","task_id":"T-3","agent_id":"claude-1",   // brief_id, task_id, agent_id omitted when empty
 "links":{"thread":"task:T-3","pr":"https://..."},   // keys omitted when empty
 "priority":70,"created_at":"...","actions":["answer","skip"]}
```

Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`. Priority is the base for the kind (plan 80, question 70, merge 60, unblock 50, stale_agent 40) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.

| kind | actions | payload → `result` |
|---|---|---|
| `question` | `answer`, `skip` | `answer` `{"answer":"..."}` (required) → Question |
| `plan` | `approve`, `reject`, `skip` | `approve` `{"note":""}` → Plan; `reject` `{"reason":""}` (or `note`) → Plan |
| `merge` | `merge`, `skip` | `merge` `{"note":""}` → Task (marks it `done`, like `POST /api/v1/tasks/{id}/done`) |
| `unblock` | `unblock`, `skip` | `unblock` `{"status":"ready or assigned","note":""}` → Task (default `assigned` when the task has an assignee, else `ready`) |
| `stale_agent` | `skip` | – |

`skip` takes `{"snooze":"1h"}` (Go duration, above 0 and up to 168h, default 1h) and returns `{"snoozed_until":"..."}`. Snoozes are held in memory.

### Chat
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/messages` | `{"to":"agent:<id> or brief:<id> or leader","body":"..."}` → Conversation (201). `body` is required and at most 64 KiB; unknown fields are rejected with 400. |
| GET | `/api/v1/conversations` | → `{"conversations":[Conversation]}` newest first, without `messages`. |
| GET | `/api/v1/conversations/{id}` | → Conversation with `messages`. 404 for an unknown id. |

```jsonc
// Conversation
{"id":"C-0123456789abcdef","to":"agent:claude-1","recipients":["claude-1"],"created_at":"...",
 "messages":[/* Message */]}                    // omitted in the list
```

A send appends one owner `message` to the agent thread of each recipient. `agent:<id>` is one agent (404 if unknown, 409 if revoked), `brief:<id>` every active agent working on that brief, `leader` the first active leader (404 when there is none). `component:<id>` is accepted but resolves no agents yet, so it returns 404. The stored and POST-returned body is prefixed with `[conv <id>] `; `GET /conversations/{id}` strips the prefix and returns the owner message plus the recipient's `message` replies posted before the owner's next message, ordered by time. Conversations are held in memory per server process.

### Prompts
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/prompts?tag=` | → `{"prompts":[Template]}` sorted by id. |
| GET | `/api/v1/prompts/{id}` | → Template. 404 for an unknown id. |
| GET | `/api/v1/prompts/{id}/render?<variable>=<value>` | → `{"id":"...","text":"..."}`; 400 listing the missing variables. |

```jsonc
// Template
{"id":"make-10x-faster","title":"Make it 10x faster","tags":["performance"],"variables":["target","command"],
 "body":"Make {{target}} at least 10x faster. ..."}
```

Built-in templates are embedded; `.md` files in `SUBSPACE_PROMPTS_DIR` override or extend them by id.

### Hillclimb
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/hillclimb/jobs` | `{"brief_id","repo","metric_cmd","direction":"min or max","target":null,"budget":0,"plateau_n":0}` → Job (201). `brief_id`, `repo`, `metric_cmd` and `direction` are required; `budget` and `plateau_n` must not be negative. |
| GET | `/api/v1/hillclimb/jobs` | → `{"jobs":[Job]}` (no filters). |
| GET | `/api/v1/hillclimb/jobs/{id}/series` | → `{"job":Job,"points":[Point]}`, oldest attempt first. 404 for an unknown id. |

```jsonc
// Job
{"id":"H-1","brief_id":"B-1","repo":"SigmaUno/foo","metric_cmd":"make bench","direction":"min",
 "target":null,"budget":0,"plateau_n":0,           // budget 0 = unlimited, plateau_n 0 = off
 "status":"running","stop_reason":null,            // running|stopped; target_reached|budget_exhausted|plateau
 "best":null,"best_attempt_id":null,"attempts":0,"since_best":0,"created_at":"..."}

// Point
{"n":1,"attempt_id":1,"agent_id":"claude-1","branch":"sub/t-3-speedup","value":12.5,"best":12.5,"measured_at":"..."}   // value null when the attempt failed
```

Jobs are held in memory per server process, and the server has no measurement runner configured, so jobs stay `running` with no attempts.

### Artifacts
Owner access is unrestricted. Scopes are `agent:<id>`, `task:<id>` and `project:<brief id>`. Agents use the MCP artifact tools.

| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/artifacts?scope=&name=` | `multipart/form-data` with the file in part `file` (optional parts `scope`, `name`, `content_type` before it) → Artifact (201). 413 `too_large` above `SUBSPACE_ARTIFACT_MAX_FILE_BYTES` (default 100 MiB), 507 `quota_exceeded` above `SUBSPACE_ARTIFACT_PROJECT_QUOTA_BYTES` per project (default 5 GiB). |
| GET | `/api/v1/artifacts?scope=&limit=` | → `{"artifacts":[Artifact]}`; `limit` default 100, max 1000. |
| GET | `/api/v1/artifacts/{id}` | → the raw file with `Content-Type`, `Content-Disposition`, `ETag` (SHA-256), `Content-Security-Policy: sandbox`; supports `Range`. |
| DELETE | `/api/v1/artifacts/{id}` | → 204 |

```jsonc
// Artifact
{"id":"A-1","scope":"task:T-3","name":"run.log","size":5,"sha256":"2cf2...","content_type":"text/plain",
 "uploaded_by":"owner","project":"B-1","created_at":"..."}
```

Files live under `$SUBSPACE_DATA_DIR/artifacts` (default: the database directory).

### Bots
Agent management with a liveness flag. A bot is an Agent plus `live` (active and seen within twice the lease, or `SUBSPACE_BOTS_LIVE_WINDOW`). `kind` accepts `claude-code`, `codex`, `opencode`, `opencode-anthropic`, `opencode-deepseek` and `other`; `provider` defaults from the kind and `role` defaults to `worker`.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/bots?role=&status=` | → `{"bots":[Bot]}` |
| POST | `/api/v1/bots` | `{"name","role","kind","provider","model"}` → `{"bot":Agent,"connect":Connect}` (201; token shown once). |
| POST | `/api/v1/bots/bulk` | `{"prefix","count","role","kind","provider","model"}` → `{"bots":[{"bot":Agent,"connect":Connect}]}` (201). |
| POST | `/api/v1/bots/spawn` | `{"brief_id","kind","model","n":1,"role","provider"}` → `{"spawns":[Spawn]}` (201). Same checks as `request_spawn`; `kind` `other` is rejected. |
| GET | `/api/v1/bots/{id}` | → `{"bot":Bot}` |
| DELETE | `/api/v1/bots/{id}` | → `{"bot":Bot}` revoked. 409 if already revoked. |
| POST | `/api/v1/bots/{id}/revoke` | `{"reason":""}` → `{"bot":Bot}`; same as DELETE. |
| POST | `/api/v1/bots/{id}/rotate-token` | `{}` → `{"bot":Agent,"connect":Connect}` (new token once). |
| POST | `/api/v1/bots/{id}/pause` | `{"reason":""}` → `{"bot":Bot}` paused (default reason `paused by owner`). 409 if revoked. |
| POST | `/api/v1/bots/{id}/resume` | `{}` → `{"bot":Bot}` |
| POST | `/api/v1/bots/{id}/kill` | `{"reason":""}` → `{"bot":Bot,"stopped":true}`. Pauses the bot (default reason `killed by owner`), then asks the spawner to stop it; `stopped` is `false` when the spawner cannot stop processes. |

### Instructions
Versioned owner instructions, appended to every agent's `get_instructions`. Scope is `global` or `project:<brief id>`; kind is `rules`, `style` or `templates`. Every save and rollback creates a new version and is audited.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/instructions/effective?project=<brief id>` | → `{"text":"# Rules\n\n...","version":"g3.0.0/p1.0.0"}`. Global text merged with the project text, one section per kind. `version` lists the head version of rules.style.templates per scope. |
| GET | `/api/v1/instructions/{scope}/{kind}` | → Version (head). 404 when never saved. |
| PUT | `/api/v1/instructions/{scope}/{kind}` | `{"text":"...","message":"..."}` → Version (201). `text` at most 256 KiB. |
| GET | `/api/v1/instructions/{scope}/{kind}/versions` | → `{"versions":[Meta]}` newest first. |
| GET | `/api/v1/instructions/{scope}/{kind}/versions/{n}` | → Version. 404 for an unknown version. |
| GET | `/api/v1/instructions/{scope}/{kind}/diff?from=&to=` | → `{"from":1,"to":2,"lines":[{"op":"equal or add or delete","text":"..."}]}` |
| POST | `/api/v1/instructions/{scope}/{kind}/rollback` | `{"to":1,"message":""}` → Version (201), a new version with the old text. 409 if `to` is already the head. |

```jsonc
// Version (Meta without "text")
{"scope":"global","kind":"rules","version":2,"author":"owner","message":"...","created_at":"...","text":"..."}
```

### Search
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/search/?q=&kind=&limit=` | → `{"results":[{"kind":"task","id":"T-1","field":"title","snippet":"...«match»...","at":"..."}]}`. `q` is required, at most 200 characters; `kind` is `task`, `message`, `agent` or `brief`; `limit` default 20, max 100. Message ids are the numeric message id. Other methods return 405. |

### Notifications
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/notify/status` | → `{"enabled":false,"sinks":[],"sent":0,"failed":0,"dropped":0,"queued":0}` |

Events form a hash chain: `hash = hex(sha256(prev_hash || canonical))`, where `prev_hash` is the previous event's hex hash (`""` for the first event) and `canonical` is the compact JSON `{"id","ts","agent_id","role","tool","task_id","before","after","note"}` in that order, with `ts` in Unix milliseconds, absent `task_id`/`note` as `""`, absent `before`/`after` as `null`, and object keys sorted.

| Method | Path | Response |
|---|---|---|
| GET | `/api/v1/audit/verify` | `{"ok":true,"events":n,"head":{"id":n,"hash":"..."}}`; when broken, `"ok":false` and `"broken_id":<first bad id>` with `events`/`head` covering the valid prefix. |
| GET | `/api/v1/audit/export?since=<id>` | `application/x-ndjson`. First line `{"chain":{"since":<id>,"prev_hash":"<hash of the last event <= since>","head":{"id","hash"}}}`, then every AuditEvent with `since < id <= head.id`, ascending. |

`subspace audit-verify --db <path>` checks the chain offline and exits non-zero at the first broken id.

## 4. SSE: `GET /api/v1/events`

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/events?topics=` | → `text/event-stream` (owner token header) |

(Browsers: proxy via the Next.js server; `EventSource` cannot set headers.)
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
| `message.created` | Message | thread:*, plus questions for kind `question` |
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
| list_artifacts, get_artifact, put_artifact | ✓ | ✓ | ✓ | ✓ |

Owner-only actions (approve_plan, reject_plan, answer_question, set_budget, register_agent(s), revoke_agent, pause_all, resume_all, edit_charter, create_brief, set_capacity, mark done) are REST/CLI only.

### Tool input schemas (JSON Schema, abbreviated)

All tools return an object; `task` = Task object above.

**Common**
- `get_instructions {}` → `{"instructions":"<text>","role":"worker","agent_id":"..."}`. The text is the built-in role instructions, then the effective owner instructions (`--- Owner instructions (<version>) ---`, when any are saved, see Instructions), then the charter.
- `whoami {}` → `{"agent":Agent,"paused":false,"server_time":"..."}`
- `list_my_tasks {"status?":["in_progress",...]}` → `{"tasks":[Task]}` (tasks where I am assignee or reviewer)
- `get_task {"task_id"}` → `{"task":Task}` (own/assigned tasks; leader: any)
- `heartbeat {"task_id?","note?"}` → `{"ok":true,"paused":false,"lease_expires_at":"..."|null}`. Extends leases of my in_progress tasks (or only `task_id`). Cheap (in-memory, batched).
- `update_status {"task_id","status":"in_progress|blocked","progress_pct?":0-100,"note?","cost_eur?":number}` → `{"task":Task}`. `cost_eur` is cumulative cost spent. Exceeding task/brief budget forces `blocked`. Cannot set submitted/approved/done.
- `add_note {"task_id?","body"}` → `{"message":Message}` (task thread, or own agent thread if no task)
- `ask_owner {"task_id?","question","options?":["..."]}` → `{"question":Question}`; answer arrives in the task/agent thread (`read_thread`).
- `post_message {"thread":"task:T-3","body"}` → `{"message":Message}`. `thread` is `agent:<id>` (or `agent:me`), `task:<id>`, `brief:<id>`. Scope: own agent thread; task threads where I am assignee/reviewer; leader additionally every task/brief thread.
- `read_thread {"thread","since?":int,"limit?":int}` → `{"messages":[Message]}`

**Artifacts** (all roles; the same access rules as the owner REST artifacts, limited to scopes the agent can reach: its own agent scope, tasks it holds, and their projects)
- `list_artifacts {"scope?","limit?":1-1000}` → `{"artifacts":[Artifact]}`
- `get_artifact {"id"}` → `{"artifact":Artifact,"content_base64":"..."}` for files up to 256 KiB, otherwise `{"artifact":Artifact,"url":"...","url_expires_at":"..."}`
- `put_artifact {"scope","name","content_base64","content_type?"}` → `{"artifact":Artifact}`

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
- `propose_plan {"brief_id","note?","tasks":[{"ref?","title","spec","acceptance_criteria":["..."],"repo","depends_on?":["<ref or T-id>"],"role?":"worker|tester","budget?":{"max_minutes","max_cost_eur"},"priority?":int,"component_id?":"C-1"}]}` → `{"plan":Plan}`. `ref` lets tasks depend on each other inside the plan. Supersedes any earlier proposed plan. The plan is linted first: findings with severity `error` (`dependency_cycle`, `unreachable_task`, `duplicate_ref`, `repo_outside_brief`, `spec_size` above 20000 characters) reject it with `bad_request` listing them; findings with severity `warning` (`acceptance_criteria`, `verification`, `spec_size`, `no_tester`, `duplicate_title`, `budget_missing`, `parallelism`) are stored in `Plan.warnings` and shown in the plan's decision summary.
- `create_task {"brief_id", ...task fields incl. "priority?", "component_id?"}` → `{"task":Task}` (status `ready` if the brief has an approved plan, else `backlog`)
- `update_task {"task_id","title?","spec?","acceptance_criteria?","depends_on?","budget?","status?":"blocked|ready|cancelled"}` → `{"task":Task}` (`ready` on a `blocked` task unblocks it: → `assigned` if it has an assignee, else `ready`; `backlog` tasks only become ready through owner plan approval)
- `assign {"task_id","agent_id","as?":"assignee|reviewer"}` → `{"task":Task}` (`ready → assigned`; reviewer on `submitted` → `in_review`). Checks agent role, `active`, provider allowed by brief; reviewer provider ≠ worker provider.
- `reassign {"task_id","agent_id","reason?"}` → `{"task":Task}` (releases lease; → `assigned`)
- `board {"brief_id?"}` → `{"columns":{status:[Task]}}`
- `list_agents {"role?","status?"}` → `{"agents":[Agent]}`
- `capacity {}` → `{"global_max_agents":n,"limits":[CapacityLimit],"agents_in_use":n,"paused":bool}`
- `request_spawn {"brief_id","worker_kind":"claude-code|codex|opencode","model","n":1-50,"role?":"worker|tester","provider?"}` → `{"spawns":[Spawn]}`; 409 `capacity` with the reason when capacity / max_parallel_agents / provider allow-list / budget / pause denies it.

### Threads scoping summary
Agent thread `agent:<id>`: leader messages to the agent, owner replies, answers, system notices. Task thread `task:<id>`: status changes (auto), notes, questions/answers, reviews, tests. Brief thread `brief:<id>`: plan decisions, leader notes.

