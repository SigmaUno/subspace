# Subspace API contract (v1)

This is the contract between the Go server and its clients (Next.js UI, `subspacectl`, agents). Server code must match it; if
the server changes, this file changes in the same PR.

- Base URL: `SUBSPACE_PUBLIC_URL` (e.g. `https://subspace.example.com`). Local default `http://localhost:8080`.
- All bodies are JSON (`Content-Type: application/json`), UTF-8. Timestamps are RFC 3339 UTC (`2026-11-20T21:04:05Z`); absent = `null`.
- IDs are short strings: briefs `B-<n>`, plans `P-<n>`, tasks `T-<n>`, questions `Q-<n>`, help requests `HR-<n>`, spawns `S-<n>`, components `C-<n>`, artifacts `A-<n>`, hillclimb jobs `H-<n>`, conversations `C-<16 hex>`. Messages and audit events have integer ids. Agent ids are chosen by the owner (`[a-z0-9][a-z0-9_-]{0,62}`).
- Heartbeat data is held in memory and flushed to the DB every ~2s. REST reads overlay the in-memory values, so `last_heartbeat_at` / `lease_expires_at` are always current.

## 1. Auth

| Surface | Credential | Notes |
|---|---|---|
| `/api/v1/*` | `Authorization: Bearer <owner token>` | Owner token from env `SUBSPACE_OWNER_TOKEN` or `SUBSPACE_OWNER_TOKEN_FILE`. Agent tokens are rejected here (403). |
| `/mcp` | `Authorization: Bearer <agent token>` | One token per agent. The owner token is rejected here (403): owner actions are never reachable through MCP. |
| `/healthz` | none | `{"status":"ok","version":"...","paused":false}` |
| `POST /hooks/github`, `POST /hooks/forgejo` | HMAC signature (`X-Hub-Signature-256`; `X-Forgejo-Signature` / `X-Gitea-Signature`) | Mounted only when `SUBSPACE_WEBHOOK_SECRET_GITHUB` / `SUBSPACE_WEBHOOK_SECRET_FORGEJO` is set. Body ≤ 1 MiB, `*-Delivery` id required, replayed delivery ids → 202 without effect. |
| `GET /a/{token}`, `POST /a/{token}` | signed one-time action token in the path | See Action links. Rate limited per client address. |

Agent tokens are 32 random bytes, base64url, prefixed `ssp_`. Only SHA-256 hashes are stored; the plaintext is returned exactly once (register / rotate). Comparison is constant-time.

Optional network allowlists (unset = unrestricted): `SUBSPACE_OWNER_ALLOW_CIDRS` limits `/api/v1/*`, `SUBSPACE_AGENT_ALLOW_CIDRS` limits `/mcp`. Entries are CIDRs or bare addresses, comma-separated. A request from another address gets 403 `forbidden`. `/healthz` is always open. The client address is the TCP peer, unless the peer is inside `SUBSPACE_TRUSTED_PROXIES` (CIDRs or addresses, default none). Then the rightmost `X-Forwarded-For` entry that is not a trusted proxy is used, and a malformed entry fails closed. A malformed allowlist denies that surface entirely.

Errors (REST): HTTP status + body
```json
{"error": {"code": "not_found", "message": "task T-9 not found"}}
```
Codes: `bad_request` 400, `unauthorized` 401, `token_expired` 401 (agent token past `token_expires_at`), `forbidden` 403, `not_found` 404, `conflict` 409 (invalid transition / duplicate), `paused` 423, `capacity` 409, `rate_limited` 429, `internal` 500, plus `too_large` 413 and `quota_exceeded` 507 (artifact uploads). Unknown `/api/v1/` paths return `not_found` with message `no such endpoint`.

Missing/invalid bearer → 401 with `WWW-Authenticate: Bearer`. An agent token past its `token_expires_at` is rejected like a revoked one, but on `/mcp` the 401 body is `{"error":{"code":"token_expired","message":"agent token expired"}}` and `WWW-Authenticate` carries `error="invalid_token"`.

In the Compose deployment, Caddy routes `/api/v1/*` (including SSE) through Next.js. The proxy adds the bearer from the browser's httpOnly owner cookie. It also accepts an explicit bearer from CLI/API clients without a session cookie, so `SUBSPACE_URL` can point to the public Caddy URL. Next validates both with Go’s owner state endpoint before forwarding requests to protected handlers. Go also authenticates each protected request.

## 2. Objects

```jsonc
// Brief
{"id":"B-1","title":"...","body":"markdown brief","repos":["SigmaUno/foo"],"status":"open",   // open|closed
 "budget":{"max_cost_eur":50.0,"max_parallel_agents":10},
 "allowed_providers":["anthropic","openai","deepseek"],   // [] = all
 "cross_review":true,"auto_done_on_merge":false,
 "cost_used":3.2,"plan_status":"approved",                 // none|proposed|approved|rejected; cost_used includes cancelled tasks
 "created_at":"..."}

// Task
{"id":"T-3","brief_id":"B-1","plan_id":"P-1","title":"...","spec":"...","acceptance_criteria":["..."],
 "repo":"SigmaUno/foo","branch":"sub/t-3-add-login","role":"worker",      // worker|tester
 "assignee":"claude-1","reviewer":null,"depends_on":["T-1"],
 "status":"in_progress",
 "progress_pct":40,"last_heartbeat_at":"...","lease_expires_at":"...","pr_url":null,
 "budget":{"max_minutes":120,"max_cost_eur":5.0},"cost_used":0.4,"minutes_used":12,
 "review_rounds":0,"priority":0,"component_id":"","lease_expiries":0,"tests":null,                       // or {"passed":10,"failed":1,"reported_at":"..."}
 "blocked_reason":null,                                // string when status is blocked or needs_owner
 "created_at":"...","updated_at":"..."}
// status: backlog|ready|assigned|in_progress|submitted|in_review|changes_requested|approved|done|blocked|needs_owner|cancelled
// Task.blocked_reason is `poison_task: ...` once `lease_expiries` reaches the poison threshold (default 3). Owner reopen resets `lease_expiries`.
// Agent.pause_reason `flapping`: 3 lease expiries within 30 minutes; the owner resumes.

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
 "last_seen_at":"...","created_at":"...","spawned":false,
 "token_expires_at":null}                          // RFC 3339 when the token expires, null = never

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
| POST | `/api/v1/backup` | `{}` → `{"path":"/data/subspace/backups/subspace-20261120T210405Z.db","bytes":12345}` (`VACUUM INTO`, then `PRAGMA integrity_check` on the produced file; a failing file is deleted). 409 if a backup for the same second exists. |
| GET | `/api/v1/backups` | → `{"backups":[{"name","path","bytes","verified":true,"created_at","age_seconds"}]}`, newest first. `verified` is the result of `integrity_check` on the file. Retention (`SUBSPACE_BACKUP_KEEP`) prunes older files after each successful backup. With Postgres the file is a JSON-lines dump of every table, verified by parsing it; backups need `SUBSPACE_BACKUP_DIR` and answer 400 without it. |

### Organization spend limits
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/limits` | → Limits |
| PUT | `/api/v1/limits` | `{"daily_cost_eur":10,"weekly_cost_eur":50}` → Limits. Replaces both caps (`0` or absent = off); 400 on a negative cap or an unknown field. Appends a `set_limits` audit event and re-evaluates immediately. |

```jsonc
// Limits
{"daily_cost_eur":10,"weekly_cost_eur":50,"org_budget_paused":false,   // org_budget_paused: pause_all with reason org_budget is active
 "windows":[{"window":"daily","cap_eur":10,"spent_eur":8.2,"percent":82,"level":"warning",   // off|ok|warning|exceeded
             "starts_at":"2026-11-21T00:00:00Z","resets_at":"2026-11-22T00:00:00Z",
             "projected_exhaustion_at":"2026-11-21T20:10:00Z"},                                // null when unknown or after resets_at
            {"window":"weekly","cap_eur":50,"spent_eur":8.2,"percent":16.4,"level":"ok",
             "starts_at":"2026-11-16T00:00:00Z","resets_at":"2026-11-23T00:00:00Z","projected_exhaustion_at":null}]}
```

The guard covers all briefs. Windows are UTC calendar windows: the day from 00:00, the week from Monday 00:00. Spend in a window is the larger of the usage records (converted to EUR) and the task `cost_used` increases first seen in it, so reported spend is not counted twice. It is evaluated every 10s and after every `update_status` call.

At 80% of a cap the guard raises a `budget` decision (`budget:org-daily`, `budget:org-weekly`) and one `spend.warning` event, delivered by the notification sinks. At 100% it calls pause_all once per window and cap with reason `org_budget`: claims and spawns are refused (423 `paused`). The pause lifts when the window resets or a raised cap puts all windows under 100%, but only while the pause reason is still `org_budget`; an owner pause is never lifted. Resuming manually keeps the guard from pausing again in the same window with the same cap. The caps and the ledger are stored in the key-value table.

### Briefs
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/briefs` | `{"title","body","repos":[],"max_cost_eur":50,"max_parallel_agents":10,"allowed_providers":[],"cross_review":true}` → Brief (201). `cross_review` defaults to `true`. |
| GET | `/api/v1/briefs?status=` | → `{"briefs":[Brief]}` |
| GET | `/api/v1/briefs/{id}` | → Brief |
| PATCH | `/api/v1/briefs/{id}` | any of `max_cost_eur, max_parallel_agents, allowed_providers, cross_review, auto_done_on_merge, status` → Brief (set_budget) |
| | | `status` is `open` or `closed`. Closing cancels `backlog`/`ready`/`assigned` tasks and moves `in_progress` tasks to `blocked` (`blocked_reason` `brief_closed`, lease released), each with a system message in the task thread; `submitted`, `in_review`, `approved` and every other status stay for the owner to finish. A closed brief rejects claims, spawns, plans and new tasks (409), and its board stays readable. The brief thread gets a system summary (cancelled/blocked counts, tasks per status, cost used). Reopening posts a brief-thread message; tasks cancelled by closing stay cancelled and `brief_closed` tasks stay blocked until unblocked. Setting the current status again changes nothing. Audit events: `close_brief` (one for the brief, one per affected task) and `reopen_brief`. |
| GET | `/api/v1/briefs/{id}/board` | → `{"brief":Brief,"columns":{"backlog":[Task],"ready":[...],...all 12 statuses...}}`; tasks in each column and in task lists are ordered by `priority` descending, then id. |
| GET | `/api/v1/briefs/{id}/architecture` | → `{"brief_id","nodes":[{"id":"C-1","brief_id","name","description","paths":[],"depends_on":["C-2"],"owners":[{"agent_id","role","agent_status","task_ids":[]}],"status":{"counts":{status:n},"total","progress_avg","blocked","stale","last_activity"}}],"edges":[{"from":"C-1","to":"C-2"}]}` |
| PUT | `/api/v1/briefs/{id}/components` | `{"components":[{"name","description","paths":[],"depends_on":["<component name>"]}]}` → `{"components":[Component]}`. Replaces the plan; ids `C-<n>` follow list order. 400 on an empty plan, duplicate names, unknown/self/cyclic dependencies or paths that are absolute or escape the repository. |
| POST | `/api/v1/briefs/import?dry_run=` | Brief document as the raw body (`text/markdown`), or `{"markdown":"..."}` with `Content-Type: application/json` → `{"brief":Brief,"components":[Component]}` (201). With `dry_run=true` nothing is created → `{"preview":BriefPreview}` (200). The whole document is validated before the brief and its component plan are created. 400 with the error position, 413 `too_large` above 1 MiB. |
| GET | `/api/v1/brief-templates` | → `{"templates":[BriefTemplate]}` sorted by id: `bugfix`, `docs-free-chore`, `feature`, `performance`, `refactor`, `security-audit`. |
| GET | `/api/v1/brief-templates/{id}/render?<variable>=<value>` | → `{"id":"...","markdown":"..."}`, a brief document ready for import. 400 listing missing variables, or when a frontmatter variable has a line break or control character; 404 for an unknown id. |

A brief document is markdown with a frontmatter block between two `---` lines; the text after the closing line is the brief body.

```markdown
---
title: "Webhooks: PR status sync"       # required
repos: [SigmaUno/subspace]
max_cost_eur: 120
max_parallel_agents: 8                  # 0 or absent = 10
allowed_providers: [anthropic, openai]  # [] or absent = all
cross_review: true                      # absent = true
task_budget:
  max_minutes: 120
  max_cost_eur: 10
components:                             # optional; same rules as PUT components
  - name: store
    paths: [internal/store]
  - name: webhook
    description: signed PR event handler
    paths:
      - internal/webhook
    depends_on: [store]
---
Markdown body for the leader.
```

The frontmatter is a strict YAML subset: block mappings and lists indented with spaces, `[a, b]` lists of scalars, plain, `"double"` (escapes `\" \\ \/ \n \t`) and `'single'` quoted scalars, `#` comments. Tabs, control characters, flow mappings, nested `[ ]`, block scalars, anchors, aliases, tags, duplicate or unknown keys, plain values containing `": "`, quoted numbers or booleans, more than 1000 list items or nesting deeper than 6 levels are rejected. Numbers are non-negative; booleans are `true`/`false`. `task_budget` is appended to the stored body as a `## Task budget defaults` section. Parse errors add the 1-based position to the error body: `{"error":{"code":"bad_request","message":"line 4, column 1: duplicate key \"title\" (first defined on line 2)","line":4,"column":1}}`; component plan errors point at the `components` key.

```jsonc
// BriefPreview
{"title":"...","body":"...","repos":[],"max_cost_eur":0,"max_parallel_agents":10,"allowed_providers":[],"cross_review":true,
 "task_budget":{"max_minutes":120,"max_cost_eur":10},   // null when absent
 "components":[{"name","description","paths":[],"depends_on":[]}]}

// BriefTemplate
{"id":"feature","title":"Feature","variables":["title","repo","goal","acceptance"],"markdown":"---\ntitle: \"{{title}}\"\n..."}
```

Template variables are `{{name}}` placeholders. Values substituted into the frontmatter are escaped for double-quoted strings.

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
| POST | `/api/v1/tasks/bulk` | `{"ids":["T-1","T-2"],"action":"cancel","params":{}}` → `{"action","results":[{"id","ok","error"?:{"code","message"}}],"succeeded","failed"}`. See Bulk operations. |

### Pull request status
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/tasks/{id}/pr` | → PRStatus, the latest snapshot of the task's PR. 404 for an unknown task or a task never synced. |
| GET | `/api/v1/prs?brief_id=&state=&limit=` | → `{"prs":[PRStatus]}` ordered by task id; `state` is `open`, `merged` or `closed`; `limit` default 100, max 1000, 400 if not a positive integer. |

```jsonc
// PRStatus
{"task_id":"T-3","brief_id":"B-1","url":"https://github.com/o/r/pull/7","host":"gh","repo":"o/r","number":7,
 "state":"open",                                   // open|merged|closed; "" until the first successful fetch
 "draft":false,"checks":"success",                 // success|failure|pending|none
 "mergeable":true,                                 // null while the host computes it
 "head_sha":"0a1b2c...","reviews":[{"user":"alice","state":"approved"}],   // latest decisive review per user: approved|changes_requested
 "review_decision":"approved",                     // approved|changes_requested|none
 "merge_ready":false,"error":null,                 // error: last failed fetch, cleared on success
 "checked_at":"...","changed_at":"..."}
```

The snapshots are stored in the database and survive restarts. A poller runs when `SUBSPACE_PRSYNC_CONFIG` names a JSON file; every `SUBSPACE_PRSYNC_INTERVAL` (default `30s`, overrides the file's `interval`) it fetches the PR of each `submitted`, `in_review` and `approved` task with a `pr_url`: the pull request, its reviews and the head commit's statuses (plus check runs on GitHub). Least recently checked tasks go first.

```jsonc
{"interval":"30s",
 "rate":{"per_minute":600,"burst":60},             // global request budget (default shown; per_minute 0 = unlimited)
 "hosts":{"gh":{"kind":"github","token_env":"GITHUB_TOKEN","rate":{"per_minute":120,"burst":20}},   // url default https://github.com, api_url default https://api.github.com (or <url>/api/v3)
          "forge":{"kind":"forgejo","url":"https://forge.example.com","token":"..."}},             // api_url default <url>/api/v1
 "repos":{"SigmaUno/subspace":"gh"}}               // repo → host; unmapped repos use the host whose url host matches the PR url
```

A PR is only fetched when its repo is the task's `repo` and, for a mapped repo, its url host is the mapped host's `url` host; otherwise `error` is set and nothing is requested. Webhook events likewise only affect tasks whose `repo` is the event's repository.

`token_env` reads the token from that environment variable; tokens are only sent as `Authorization` headers and never logged or stored. Requests are conditional (`If-None-Match` with the last `ETag`, cached in memory). Each request takes a token from the global and the host bucket; when one is empty the task waits for the next cycle. A host answering 429 (or 403 with `X-RateLimit-Remaining: 0`) is paused for `Retry-After` / until `X-RateLimit-Reset`. Other failures set `error` and back the task off with jitter: a random delay between half and all of `interval × 2^(failures-1)`, capped at 32 intervals.

When a snapshot changes the server emits `task.pr` (data PRStatus, topics `board`, `brief:<id>`, `task:<id>`). Transitions post a `system` message by `prsync` to the task thread: checks failed, head force-pushed (the previous head is no longer in the PR's commits), closed without merging, and merged. On merge a brief with `auto_done_on_merge` marks the task `done` (from `approved` or `needs_owner`; otherwise the message says why it was not). An `approved` task whose PR is open, not a draft, mergeable, with checks `success` and no `changes_requested` review has `merge_ready` and appears in the decisions feed as kind `merge-ready` (id `merge-ready:<task id>`, links to the PR, action `skip` only).

### Agents
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/agents?role=&status=` | → `{"agents":[Agent]}` |
| GET | `/api/v1/agents/{id}` | → Agent |
| POST | `/api/v1/agents` | `{"id","role","kind","model","provider","expires_in":""}` → `{"agent":Agent,"connect":Connect}` (201; `connect.token` shown once). `provider` defaults from kind (claude-code→anthropic, codex→openai, opencode→deepseek). `expires_in` is an optional positive Go duration (e.g. `720h`) after which the token stops working; empty = never expires; 400 otherwise. |
| POST | `/api/v1/agents/bulk` | `{"prefix":"claude","count":50,"role":"worker","kind":"claude-code","model":"...","provider":"","expires_in":""}` → `{"agents":[{"agent":Agent,"connect":Connect}]}`. Ids `<prefix>-001`…; max count 1000; atomic; `expires_in` applies to every agent. |
| POST | `/api/v1/agents` | `{"id","role","kind","model","provider"}` → `{"agent":Agent,"connect":Connect}` (201; `connect.token` shown once). `provider` defaults from kind (claude-code→anthropic, codex→openai, opencode→deepseek); it must match `[a-z0-9][a-z0-9._-]{0,62}` and, for claude-code and codex, equal the kind's provider (400 otherwise). |
| POST | `/api/v1/agents/bulk` | `{"prefix":"claude","count":50,"role":"worker","kind":"claude-code","model":"...","provider":""}` → `{"agents":[{"agent":Agent,"connect":Connect}]}`. Ids `<prefix>-001`…; max count 1000; atomic. |
| GET | `/api/v1/agents/{id}/connect` | → Connect without token (snippets keep `<token>`). |
| POST | `/api/v1/agents/{id}/rotate-token` | `{"expires_in":""}` → `{"agent":Agent,"connect":Connect}` (new token once; old one dead). Without `expires_in` the current `token_expires_at` is kept (400 when it has already passed); with it the expiry is set to now plus the duration. |
| POST | `/api/v1/agents/{id}/revoke` | `{"reason":""}` → Agent. Token dead immediately, leases released. |
| POST | `/api/v1/agents/{id}/resume` | `{}` → Agent (clears loop-protection pause) |
| POST | `/api/v1/agents/bulk-action` | `{"ids":["claude-1"],"action":"pause","params":{}}` → `{"action","results":[{"id","ok","error"?,"connect"?}],"succeeded","failed"}`. See Bulk operations. |

### Bulk operations
`POST /api/v1/tasks/bulk` and `POST /api/v1/agents/bulk-action` apply one action to up to 500 ids, one id at a time, in request order (duplicate ids are applied once). Each id is its own operation with its own audit event and SSE update (`task.updated` / `agent.updated`), exactly as the single-item endpoints. Partial success is allowed: the response is `200` with a result per id, `ok:false` results carry the error `code` and `message` the single-item endpoint would have returned (`not_found`, `conflict`, `forbidden`, ...). The request itself is rejected with `400` for an empty or oversized `ids`, empty id values, an unknown `action` or missing/invalid `params`; nothing is applied then.

| Endpoint | action | params |
|---|---|---|
| tasks/bulk | `cancel` | `{"reason":""}` |
| tasks/bulk | `reopen` | `{"status":"ready|assigned","note":""}` (default `ready`) |
| tasks/bulk | `unblock` | `{"status":"ready|assigned","note":""}`; only `blocked` and `needs_owner` tasks, `conflict` otherwise |
| tasks/bulk | `set_budget` | `{"max_minutes":120,"max_cost_eur":5}`; at least one, non-negative; an omitted field keeps the task's value |
| tasks/bulk | `assign` | `{"agent_id":"claude-1","as":"assignee|reviewer"}` (required `agent_id`) |
| tasks/bulk | `reassign` | `{"agent_id":"claude-1","reason":""}` (required `agent_id`) |
| agents/bulk-action | `pause` | `{"reason":""}`; `conflict` for revoked agents |
| agents/bulk-action | `resume` | `{}` |
| agents/bulk-action | `revoke` | `{"reason":""}`; `conflict` for already revoked agents |
| agents/bulk-action | `rotate-token` | `{}`; each successful result carries `connect` (Connect with the new token, shown once) |
| GET | `/api/v1/agents/{id}/stats` | → AgentStats. 404 for an unknown agent. |
| GET | `/api/v1/agent-stats?role=` | → `{"agents":[AgentStats]}` for every agent, best `score` first, then id. |

```jsonc
// AgentStats
{"agent_id":"claude-1","role":"worker","provider":"anthropic","status":"active",
 "tasks_completed":4,                         // tasks assigned to the agent that reached approved or done
 "avg_duration_seconds":1800,"p90_duration_seconds":3600,   // time in_progress per completed task, summed over claims; 0 without data
 "review_rounds_per_task":0.25,               // changes_requested reviews per completed task
 "first_pass_approval_rate":0.75,             // reviewed tasks whose first review approved; null when none was reviewed
 "blocked":1,"expired":0,                     // times a task went blocked / lost its lease while the agent held it, over all its tasks
 "cost_per_task":1.2,"tokens_per_task":5300,  // task cost_used and reported usage tokens (in + out) per completed task; tokens 0 without usage records
 "trend":[{"day":"2026-11-14","completed":0},...],   // 7 UTC days ending today, oldest first
 "score":0.71}                                // 0..1, 0.5 without history
```

`score` is `0.5 + (raw - 0.5) * n / (n + 3)` with `raw = 0.5 * first_pass_approval_rate + 0.3 * tasks_completed / n + 0.2 / (1 + review_rounds_per_task)` and `n = tasks_completed + blocked + expired` (a missing first-pass rate counts as 0.5). With `SUBSPACE_AUTOPILOT=on`, autopilot uses it to break ties between equally loaded eligible agents (higher first, then id); load, role, provider and capacity rules are never overridden. Scores used by autopilot are cached for 30s.

### Questions
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/questions?status=open` | → `{"questions":[Question]}` |
| POST | `/api/v1/questions/{id}/answer` | `{"answer":"..."}` → Question. Posts an `answer` message to the task and agent threads. |

### Help requests
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/help-requests?status=&brief_id=&task_id=&limit=` | → `{"help_requests":[HelpRequest]}` newest first. `status` is `open` (default), `accepted`, `superseded`, `closed` or `all`; `limit` default and max 1000, 400 if not a positive integer. |

```jsonc
// HelpRequest
{"id":"HR-1","type":"help",                  // help|blocker|split
 "kind":"unclear_spec",                      // help: unclear_spec|missing_access|conflicting_requirements|too_big|dependency_issue; blocker: "blocker"; split: "too_big"
 "task_id":"T-3","brief_id":"B-1","agent_id":"claude-1","role":"worker",
 "needs":"",                                 // blocker only: leader|owner
 "details":"...",                            // help text, blocker reason, or a summary of the proposed tasks
 "proposed_tasks":[],                        // split only: [{"ref","title","spec","acceptance_criteria":[],"repo","depends_on":[],"role","budget":{},"priority","component_id"}]
 "status":"open",                            // open|accepted|superseded|closed
 "created_tasks":[],                         // split accepted: ids of the tasks created
 "created_at":"..."}
```

A request is `open` while it still applies: `help` and `split` while the task is not `done` or `cancelled`, `blocker` while the task is `blocked` or `needs_owner`; otherwise `closed`. Created by the MCP tools `request_help`, `report_blocker` and `suggest_split`, each audited under its tool name.

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

Durations use Go syntax; `0` disables a rule. The `noop` spawner cannot stop or inspect processes: spawns are still marked stopped and their agents revoked, and `process` stays `unknown`. The `hc` spawner stops processes with `hc kill` and reports `process` from `hc` listings (an unparseable or truncated listing stays `unknown`). When the spawner can send text to a running agent (`hc`), a connected agent that has no `in_progress` task and gets a task in `assigned` or `changes_requested` receives one nudge per task and status, pointing it at `list_my_tasks`; nothing is sent while paused.

### Provider circuit breakers
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/breakers` | → `{"breakers":[Breaker]}` sorted by provider, then model. Only provider/model pairs that reported at least one sample appear. |
| POST | `/api/v1/breakers/{provider}/reset` | `{}` → `{"breakers":[Breaker]}`. Closes every breaker of the provider and clears its samples. 404 when the provider has no breaker. |

```jsonc
// Breaker
{"provider":"anthropic","model":"claude-sonnet-5-5","state":"open",   // closed|open|half_open
 "error_rate":0.62,"samples":13,"errors":8,"threshold":0.5,           // closed: current window; open/half_open: the window that tripped it
 "opened_at":null,"half_open_at":null}                                // opened_at null when closed; half_open_at only while open
```

Samples are usage reports (`report_usage`; `rate_limited: true` counts as an error) and `report_provider_error` calls, kept per provider/model in memory for a rolling window. A breaker opens when the window holds at least `SUBSPACE_BREAKER_MIN_SAMPLES` samples (default 10) and the error rate is above `SUBSPACE_BREAKER_ERROR_RATE` (default `0.5`, window `SUBSPACE_BREAKER_WINDOW`, default `5m`). While open, `request_spawn`, `POST /api/v1/bots/spawn` and autopilot start no agents of that provider/model (409 `capacity`) and autopilot assigns it no tasks; running agents continue. After `SUBSPACE_BREAKER_COOLDOWN` (default `1m`) it half-opens: one probe agent (`n` = 1) may be spawned and autopilot assigns one task when no agent of that provider/model has work. The next sample decides: a success closes the breaker, an error opens it again. An unused probe grant expires after another cooldown. Breakers are held in memory per server process. An open or half-open breaker appears in the decision inbox as `breaker:<provider>:<model>` (`skip` only); resetting or closing removes it.

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
The owner inbox. Decisions are derived from open questions, proposed plans, `approved` tasks awaiting a merge, `blocked`/`needs_owner` tasks, stale agents (an active agent with a current task not seen for 10 minutes) and active agents whose token expires in less than 24 hours (or already has).

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/decisions?limit=` | → `{"decisions":[Decision]}` without snoozed ones, ordered by `priority` descending, then `created_at` ascending, then `id`. `limit` default 100, max 1000, 400 if not a positive integer. |
| GET | `/api/v1/decisions/next` | → `{"decision":Decision}`; `decision` is `null` when the inbox is empty. |
| POST | `/api/v1/decisions/{id}` | `{"action":"...", ...}` → `{"id":"q:Q-1","action":"answer","result":<result>,"next":Decision}`; `next` is `null` when nothing is left. 400 without `action`, 404 for an unknown id, 409 when the action is not in the decision's `actions`. |

```jsonc
// Decision
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent|token_expiry
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent|merge-ready
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent|budget
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent|token_expiry|help
{"id":"q:Q-1","kind":"question",               // question|plan|merge|unblock|stale_agent|token_expiry|merge-ready|breaker
 "title":"...","summary":"...","brief_id":"B-1","task_id":"T-3","agent_id":"claude-1",   // brief_id, task_id, agent_id omitted when empty
 "links":{"thread":"task:T-3","pr":"https://..."},   // keys omitted when empty
 "priority":70,"created_at":"...","actions":["answer","skip"]}
```

Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`, `token:<agent id>`. Priority is the base for the kind (plan 80, question 70, merge 60, token_expiry 55, unblock 50, stale_agent 40) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.
Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`, `merge-ready:<task id>` (see Pull request status). Priority is the base for the kind (plan 80, question 70, merge 60, unblock 50, stale_agent 40) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.
Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`, `token:<agent id>`, `help:<help request id>`. Priority is the base for the kind (plan 80, question 70, merge 60, token_expiry 55, unblock 50, stale_agent 40, help 0) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.
Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`, `token:<agent id>`, `breaker:<provider>:<model>`. Priority is the base for the kind (plan 80, breaker 75, question 70, merge 60, token_expiry 55, unblock 50, stale_agent 40) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.
Ids are `q:<question id>`, `plan:<plan id>`, `merge:<task id>`, `unblock:<task id>`, `stale:<agent id>`, `merge-ready:<task id>` (see Pull request status), `breaker:<provider>:<model>`. Priority is the base for the kind (plan 80, breaker 75, question 70, merge 60, unblock 50, stale_agent 40) plus 1 per 5 minutes of age (at most 60) plus 15 for `needs_owner` tasks.

| kind | actions | payload → `result` |
|---|---|---|
| `question` | `answer`, `skip` | `answer` `{"answer":"..."}` (required) → Question |
| `plan` | `approve`, `reject`, `skip` | `approve` `{"note":""}` → Plan; `reject` `{"reason":""}` (or `note`) → Plan |
| `merge` | `merge`, `skip` | `merge` `{"note":""}` → Task (marks it `done`, like `POST /api/v1/tasks/{id}/done`) |
| `unblock` | `unblock`, `skip` | `unblock` `{"status":"ready or assigned","note":""}` → Task (default `assigned` when the task has an assignee, else `ready`) |
| `stale_agent` | `skip` | – |
| `help` | `skip` | – (an open `help` request; the agent's details are the summary) |
| `breaker` | `skip` | – |
| `token_expiry` | `rotate`, `skip` | `rotate` `{"expires_in":"720h"}` (optional, default `720h`) → `{"agent":Agent,"connect":Connect}` (same as `POST /api/v1/agents/{id}/rotate-token`; `connect.token` shown once). `created_at` is 24h before the expiry. |

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

### Task templates
A prompt library entry with `kind: task` is a task template: the leader tool `create_task_from_template` renders it into a task.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/task-templates` | → `{"templates":[Template]}` sorted by id, only entries with `kind: task`: `add-feature-with-tests`, `fix-bug-with-regression-test`, `perf-hillclimb-task`, `refactor-keep-behavior`, `security-fix`, `write-integration-tests`. |

Library files stay backwards compatible: the added front matter keys are optional and only valid with `kind: task`. Task templates add `kind`, `task_title` (required, may use `{{variable}}`), `role` (`worker` default, or `tester`), `budget_minutes`, `budget_cost_eur` (plain non-negative numbers) and `acceptance_criteria` (a block list of `- ` lines, at most 20 items of at most 500 characters, plain or `"double"` quoted). New keys may appear once, tabs in list indentation, empty items, control characters and list items outside `acceptance_criteria` are rejected, and every `{{variable}}` used in the title, body or criteria must be declared in `variables`.

```jsonc
// Template (task template fields are omitted for plain prompts)
{"id":"fix-bug-with-regression-test","title":"Fix a bug with a regression test","tags":["task-template","bugfix"],
 "variables":["bug","symptom","area"],"body":"...","kind":"task","task_title":"Fix {{bug}}","role":"worker",
 "budget":{"max_minutes":120,"max_cost_eur":5},"acceptance_criteria":["..."]}
```

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

Jobs and attempts are stored in the database. Attempts are recorded by the MCP `measure` tool, which exists only when `SUBSPACE_HILLCLIMB_WORKDIR` is set; without it jobs stay `running` with no attempts. The runner fetches the branch into a fresh git worktree under that directory and runs `metric_cmd` with `/bin/sh -c`, using only repos listed in `SUBSPACE_HILLCLIMB_REPOS` (JSON object `{"<repo>":"<git url>"}`). Other settings: `SUBSPACE_HILLCLIMB_TIMEOUT` (default `10m`), `SUBSPACE_HILLCLIMB_MAX_OUTPUT` (bytes, default 1 MiB), `SUBSPACE_HILLCLIMB_ENV` (comma list of extra env names passed through; `PATH`, `LANG`, `LC_ALL`, `TZ` always are), `SUBSPACE_HILLCLIMB_RLIMIT_CPU` (seconds), `SUBSPACE_HILLCLIMB_RLIMIT_MEM` (bytes), `SUBSPACE_HILLCLIMB_RLIMIT_NOFILE`. A timeout, an exceeded output cap, a symlink escaping the worktree or a non-zero exit records a failed attempt (`value` null).

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

Files live under `$SUBSPACE_DATA_DIR/artifacts` (default: the database directory; required when `SUBSPACE_DB` is a `postgres://` URL).

### Bots
Agent management with a liveness flag. A bot is an Agent plus `live` (active and seen within twice the lease, or `SUBSPACE_BOTS_LIVE_WINDOW`). `kind` accepts `claude-code`, `codex`, `opencode`, `opencode-anthropic`, `opencode-deepseek` and `other`; `provider` defaults from the kind and `role` defaults to `worker`.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/bots?role=&status=` | → `{"bots":[Bot]}` |
| POST | `/api/v1/bots` | `{"name","role","kind","provider","model","expires_in":""}` → `{"bot":Agent,"connect":Connect}` (201; token shown once). |
| POST | `/api/v1/bots/bulk` | `{"prefix","count","role","kind","provider","model","expires_in":""}` → `{"bots":[{"bot":Agent,"connect":Connect}]}` (201). |
| POST | `/api/v1/bots/spawn` | `{"brief_id","kind","model","n":1,"role","provider"}` → `{"spawns":[Spawn]}` (201). Same checks as `request_spawn`; `kind` `other` is rejected. |
| GET | `/api/v1/bots/{id}` | → `{"bot":Bot}` |
| DELETE | `/api/v1/bots/{id}` | → `{"bot":Bot}` revoked. 409 if already revoked. |
| POST | `/api/v1/bots/{id}/revoke` | `{"reason":""}` → `{"bot":Bot}`; same as DELETE. |
| POST | `/api/v1/bots/{id}/rotate-token` | `{"expires_in":""}` → `{"bot":Agent,"connect":Connect}` (new token once). `expires_in` as for agents. |
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

### Timeseries
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/timeseries?metric=&bucket=&since=&brief_id=` | → Series. `metric` is required: `tasks_done`, `tasks_created`, `cost_eur`, `active_agents`, `tokens` or `review_rounds`. `bucket` is `5m`, `1h` (default) or `1d`. `since` is an RFC 3339 timestamp (default: 24h for `5m`, 7d for `1h`, 30d for `1d`). `brief_id` limits the series to one brief (404 if unknown). 400 for an unknown metric or bucket, a malformed `since` or one in the future. |

```jsonc
// Series
{"metric":"tasks_done","bucket":"1h","since":"2026-11-20T00:00:00Z","until":"2026-11-21T01:00:00Z",   // until is exclusive
 "points":[{"t":"2026-11-20T00:00:00Z","v":2}]}                                                       // one point per bucket, zero when empty
```

Buckets are aligned to UTC multiples of the bucket size. A series has at most 2000 buckets: an earlier `since` is moved forward so the series ends with the bucket containing now. Metrics: `tasks_done` counts audit events that moved a task to `done`; `tasks_created` counts tasks by `created_at`; `review_rounds` counts `review` events that requested changes; `active_agents` counts distinct non-owner agents with audit events; `cost_eur` sums increases of a task's `cost_used` reported by `update_status`; `tokens` sums input and output tokens of stored usage records.

### Notifications
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/notify/status` | → `{"enabled":false,"sinks":[],"sent":0,"failed":0,"dropped":0,"queued":0}` |

When `SUBSPACE_PUBLIC_URL` starts with `https://`, notifications for proposed plans (`Approve`, `Reject`, `Snooze 1h`), questions with options (`Option 1`…`Option 3`, plus `Snooze 1h` when there are fewer than 3 options) and approved tasks (`Mark merged`, `Snooze 1h`) carry action links: the webhook body gets `"actions":[{"label":"Approve","url":"https://.../a/<token>"}]`, ntfy gets an `Actions` header with up to 3 `view` actions, Slack gets link buttons. Option labels are numbered; the option text is only shown on the confirmation page.

### Digest
| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/digest/preview?window=` | → Preview of the digest for the last `window` (Go duration above 0 and up to `744h`, default `24h`). 400 for another value. |

```jsonc
// Preview
{"digest":{"since":"2026-11-20T08:00:00Z","until":"2026-11-21T08:00:00Z",
           "tasks":{"done":3,"created":5,"blocked":1},          // blocked: tasks now blocked or needs_owner, last updated in the window
           "cost_eur":12.5,"tokens":53000,                        // reported cost increases and usage tokens in the window
           "risks":[{"source":"spend","severity":"high","subject":"daily","message":"daily spend at 100% of its cap"}],   // source spend|forecast, severity high|medium, high first
           "decisions":{"count":7,"titles":["Approve plan P-1 for B-1"]},   // pending owner decisions without merge-ready ones; up to 5 titles by priority
           "flapping_agents":["claude-2"],"merge_ready":["T-5"]},          // agents paused with reason flapping; tasks whose PR is merge-ready
 "title":"Subspace digest: 3 done, 7 decisions pending","text":"Window: ..."}
```

A digest is a short plain-text summary sent through the notification sinks (`kind` `digest`, same sinks as other notifications). It holds counts, cost, token totals, risks from the spend limits (at least 80% of a cap) and from the open briefs' forecasts, the number and titles of pending decisions, flapping agents and merge-ready PRs. Titles are shortened to 80 characters and secrets in them are masked; task specs, notes and thread messages are never included. The scheduler runs only when at least one notification sink is configured.

| Var | Default | Meaning |
|---|---|---|
| `SUBSPACE_DIGEST_AT` | – | daily digest times, `HH:MM` list such as `08:00,18:00` |
| `SUBSPACE_DIGEST_WEEKLY` | – | weekly digest times, `day@HH:MM` list such as `mon@09:00,fri@17:00` (`mon`…`sun` or full names) |
| `SUBSPACE_DIGEST_TZ` | `UTC` | IANA time zone of the times above |

Each variable is its own rule with its own window, which runs from the previous slot of that rule (or from the last evaluation) to now, at most 8 days. A time that does not exist on a daylight-saving day fires right after the gap; an ambiguous time fires once, at its first occurrence. A rule is skipped when nothing changed since its last digest: no tasks done, created or blocked, no cost or tokens, and the same pending decisions, risks, flapping agents and merge-ready PRs. A change to an empty state is sent once. The first evaluation after a fresh start only records the slot, and a missed slot is sent once on the next start. Rule state is kept in the key-value table (`digest.state.daily`, `digest.state.weekly`). Invalid values disable the digest and are logged.

### Action links
| Method | Path | Body → Response |
|---|---|---|
| POST | `/api/v1/action-links` | `{"decision_id":"plan:P-1","action":"approve","option":0,"ttl":"30m"}` → `{"url":"https://.../a/<token>","expires_at":"..."}` (201). `action` is one of the decision's `actions` (see Decisions); `option` is the 0-based option index for `answer` and must be 0 otherwise; `ttl` from `1m` to `24h`, default `30m`. 404 for a decision that is not pending, 409 when the action is not available, the option does not exist or `SUBSPACE_PUBLIC_URL` is not https. |

A token is `base64url(claims) "." base64url(HMAC-SHA256)`; the claims bind exactly one decision id, one action, the option, a random nonce and the expiry. The key is derived with HKDF-SHA256 from `SUBSPACE_ACTION_SECRET` (at least 24 bytes), or from the owner token when unset, so rotating either invalidates all links. Signatures are compared in constant time and tokens are never logged.

- `GET /a/{token}` renders a confirmation page (decision title and summary, the action, the expiry) and never performs the action, so link previews and prefetchers are harmless. It sets a `Secure`, `HttpOnly`, `SameSite=Strict` cookie holding a per-render nonce that the form echoes together with its HMAC.
- `POST /a/{token}` (form field `csrf`) checks the render nonce, records the token nonce as used, then runs the action through the same board methods as `POST /api/v1/decisions/{id}` as the owner; notes and reject reasons are `action-link`, `answer` answers with the option text, `skip` snoozes for 1h. If the action fails the nonce is released.
- Responses are HTML with `Cache-Control: no-store` and `Referrer-Policy: no-referrer`: 200 confirmation or result, 403 missing or wrong render nonce, 404 malformed, tampered or expired token, 409 decision no longer pending (decided, snoozed or the action no longer allowed), 410 already used. Concurrent submits of the same link execute once.
- `/a/` is rate limited per client address (`SUBSPACE_RATE_ACTIONLINK`, default `30/m:10`, `off` disables; the client address honours `SUBSPACE_TRUSTED_PROXIES`) and answers 429 `rate_limited` above it. Used nonces are kept in the `action_link_uses` table until their expiry.

Events form a hash chain: `hash = hex(sha256(prev_hash || canonical))`, where `prev_hash` is the previous event's hex hash (`""` for the first event) and `canonical` is the compact JSON `{"id","ts","agent_id","role","tool","task_id","before","after","note"}` in that order, with `ts` in Unix milliseconds, absent `task_id`/`note` as `""`, absent `before`/`after` as `null`, and object keys sorted.

| Method | Path | Response |
|---|---|---|
| GET | `/api/v1/audit/verify` | `{"ok":true,"events":n,"head":{"id":n,"hash":"..."}}`; when broken, `"ok":false` and `"broken_id":<first bad id>` with `events`/`head` covering the valid prefix. |
| GET | `/api/v1/audit/export?since=<id>` | `application/x-ndjson`. First line `{"chain":{"since":<id>,"prev_hash":"<hash of the last event <= since>","head":{"id","hash"}}}`, then every AuditEvent with `since < id <= head.id`, ascending. |

`subspace audit-verify --db <path>` checks the chain offline and exits non-zero at the first broken id.

## 4. SSE: `GET /api/v1/events`

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/v1/events?topics=&last_event_id=` | → `text/event-stream` (owner token header) |

(Browsers: proxy via the Next.js server; `EventSource` cannot set headers.)
Query `topics=` comma list, default `all`: `all`, `board` (every task change), `brief:<id>`, `task:<id>`, `agents`, `agent:<id>`, `thread:<kind>:<id>`, `questions`, `plans`, `control`.

Response `text/event-stream`, a `: ping` comment every 15s. Each event:
```
id: 1234
event: task.updated
data: {"type":"task.updated","ts":"2026-11-20T21:04:05Z","data":{...}}
```
`id` is a per-server monotonic sequence. First event is always `hello` (`{"seq":1234}`).

Resume: the server keeps the last `SUBSPACE_SSE_REPLAY` published events (default `10000`, `0` disables) in memory. A client that reconnects with the `Last-Event-ID` header or `?last_event_id=<id>` (the query wins when both are set) first receives `hello`, then every retained event after that id that matches the requested topics, in order, then the live stream, without gaps or duplicates. On a resumed connection `hello` has no `id`, so the client's last id stays unchanged. A non-integer or negative id is a 400 `bad_request`.

When the id is older than the oldest retained event, or newer than the server's current sequence (for example after a restart), nothing is replayed. The server sends one `resync` event (`{"seq":n}`, `id` = current sequence) right after `hello`, and the client must refetch its state before following the stream. Without a last id, refetch state on connect as before. The buffer is per process and not persisted. Events dropped by a `lagged` overflow can be recovered by reconnecting with the last received id.

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
| `spend.warning` | `{"window":"daily","cap_eur":10,"spent_eur":8.2,"percent":82,"level":"warning","starts_at":"..."}` | control |
| `capacity.changed` | capacity object (as GET /capacity) | control |
| `resync` | `{"seq":n}` — a resume could not be served; refetch | all |
| `lagged` | `{"dropped":n}` — slow-client coalescing happened; refetch | all |

On shutdown the server stops accepting connections, sends a final `close` event (`event: close`, data `{"type":"close",...,"data":{"reason":"shutdown"}}`, no `id`) on every stream and ends it (clients reconnect and refetch), lets in-flight requests finish for up to `SUBSPACE_SHUTDOWN_TIMEOUT` (default `20s`), then flushes heartbeat state to the DB and closes the store.

Slow subscribers have a bounded buffer; when it overflows, oldest events are dropped and a `lagged` event is delivered.

## 5. MCP: `POST /mcp` (agent token)

Streamable HTTP, JSON-RPC 2.0, protocol version `2025-06-18` (older `2025-03-26` accepted). One JSON-RPC message per POST (batching unsupported). `Accept` should include `application/json`; responses are `application/json` (no SSE streaming). Notifications/responses → `202`. `GET /mcp` → 405. `DELETE /mcp` with `Mcp-Session-Id` ends the session.

`initialize` returns `Mcp-Session-Id` (bound to the agent). The header is optional afterwards, but if sent and unknown/expired → 404 (client re-initializes). Methods: `initialize`, `notifications/initialized`, `ping`, `tools/list`, `tools/call`.

`tools/list` returns only tools the caller's role may call. `tools/call` re-checks; calling a tool outside the role → JSON-RPC error `-32601 method not found` style `tool not available for role`.

`tools/call` result: `{"content":[{"type":"text","text":"<json>"}],"structuredContent":{...},"isError":false}`. Tool errors: `isError:true` and `structuredContent:{"error":{"code","message"}}` (codes as REST plus `agent_paused`, `lease_expired`, `not_assigned`, `budget_exceeded`).

`get_instructions` content is stable (see SPEC §6) and ends with the owner charter.

### Idempotency keys
`claim_task`, `release_task`, `submit`, `review`, `update_status`, `report_tests`, `ask_owner`, `post_message`, `add_note`, `assign`, `reassign`, `propose_plan`, `create_task`, `create_task_from_template` and `request_spawn` accept an optional idempotency key in `params._meta.idempotencyKey` of `tools/call` (or the `Idempotency-Key` header; `_meta` wins), at most 255 bytes. Keys are scoped per agent and kept for 24h together with the tool, a hash of the canonical `arguments` and the result.
- A retry with the same key, tool and arguments returns the stored result (with the new JSON-RPC `id`) and the header `Idempotent-Replayed: true`; the call is not applied again. Concurrent duplicates wait for the first call.
- The same key with another tool or other arguments → tool error `idempotency_conflict`.
- Results with tool error codes `internal`, `agent_paused` and `rate_limited` are not stored, so a retry runs the call again.
- The key row is written right after the call's own transaction commits: if the server stops between the two, a retry applies the call again (at-least-once in that window).

`internal/client` sends a fresh key with every call to these tools and retries up to 3 times with the same key on transport errors; any call is retried on `429` after `Retry-After`.

### Tool permissions (✓ = in `tools/list` for the role)

| Tool | leader | worker | tester | reviewer |
|---|:-:|:-:|:-:|:-:|
| get_instructions, whoami, list_my_tasks, get_task, heartbeat, update_status, add_note, ask_owner, post_message, read_thread | ✓ | ✓ | ✓ | ✓ |
| claim_task, release_task, submit | | ✓ | ✓ | |
| report_tests | | | ✓ | |
| list_reviews, review | | | | ✓ |
| get_brief, list_briefs, propose_plan, create_task, create_task_from_template, update_task, assign, reassign, board, list_agents, capacity, request_spawn | ✓ | | | |
| list_artifacts, get_artifact, put_artifact | ✓ | ✓ | ✓ | ✓ |
| request_help, suggest_split | | ✓ | ✓ | ✓ |
| report_blocker | | ✓ | ✓ | |
| list_help_requests, accept_split | ✓ | | | |
| report_usage, report_provider_error | ✓ | ✓ | ✓ | ✓ |

Owner-only actions (approve_plan, reject_plan, answer_question, set_budget, register_agent(s), revoke_agent, pause_all, resume_all, edit_charter, create_brief, set_capacity, mark done) are REST/CLI only.

### Tool input schemas (JSON Schema, abbreviated)

All tools return an object; `task` = Task object above.

**Common**
- `get_instructions {}` → `{"instructions":"<text>","role":"worker","agent_id":"..."}`. The text is the built-in role instructions (for workers of kind `claude-code`, `codex` or `opencode`, followed by short kind tips), then the effective owner instructions (`--- Owner instructions (<version>) ---`, when any are saved, see Instructions), then the charter.
- `whoami {}` → `{"agent":Agent,"paused":false,"server_time":"..."}`
- `list_my_tasks {"status?":["in_progress",...]}` → `{"tasks":[Task]}` (tasks where I am assignee or reviewer)
- `get_task {"task_id"}` → `{"task":Task}` or `{"task":Task,"context":TaskContext}` (own/assigned tasks; leader: any). `context` is only present for the task's assignee, see Handoff context.
- `heartbeat {"task_id?","note?"}` → `{"ok":true,"paused":false,"lease_expires_at":"..."|null}`. Extends leases of my in_progress tasks (or only `task_id`). Cheap (in-memory, batched).
- `update_status {"task_id","status":"in_progress|blocked","progress_pct?":0-100,"note?","cost_eur?":number}` → `{"task":Task}`. `cost_eur` is cumulative cost spent (0..1000000; a lower value than the recorded cost is ignored). Exceeding task/brief budget forces `blocked`. Cannot set submitted/approved/done.
- `add_note {"task_id?","body"}` → `{"message":Message}` (task thread, or own agent thread if no task)
- `ask_owner {"task_id?","question","options?":["..."]}` → `{"question":Question}`; answer arrives in the task/agent thread (`read_thread`).
- `post_message {"thread":"task:T-3","body"}` → `{"message":Message}`. `thread` is `agent:<id>` (or `agent:me`), `task:<id>`, `brief:<id>`. Scope: own agent thread; task threads where I am assignee/reviewer; leader additionally every task/brief thread.
- `read_thread {"thread","since?":int,"limit?":int}` → `{"messages":[Message]}`

**Artifacts** (all roles; the same access rules as the owner REST artifacts, limited to scopes the agent can reach: its own agent scope, tasks it holds, and their projects)
- `list_artifacts {"scope?","limit?":1-1000}` → `{"artifacts":[Artifact]}`
- `get_artifact {"id"}` → `{"artifact":Artifact,"content_base64":"..."}` for files up to 256 KiB, otherwise `{"artifact":Artifact,"url":"...","url_expires_at":"..."}`
- `put_artifact {"scope","name","content_base64","content_type?"}` → `{"artifact":Artifact}`

**Usage and provider health** (all roles; never loop-protected)
- `report_usage {"model","provider?","tokens_in?","tokens_out?","cache_read?","cache_write?","cost_usd?","task_id?","brief_id?","rate_limited?":bool,"request_id?"}` → `{"ok":true,"record":{...}}`. `provider` is derived from well-known model names when omitted (400 otherwise). Every report is a sample for the provider circuit breaker (see Provider circuit breakers); usage records are held in memory.
- `report_provider_error {"provider?","model?","status?":int}` → `{"ok":true,"provider","model","state":"closed|open|half_open"}`. Counts one failed request; `provider` and `model` default to the calling agent's.

**Worker/tester**
- `claim_task {"task_id"}` → `{"task":Task,"lease_expires_at":"...","context?":TaskContext}`. Requires: assigned to me, status `assigned` or `changes_requested`, dependencies `done`, not paused, within brief max_parallel_agents/budget.
- `release_task {"task_id","reason?"}` → `{"task":Task}` (→ `assigned`)
- `submit {"task_id","pr_url","summary"}` → `{"task":Task}` (→ `submitted`, or → `approved` when the brief has `cross_review:false`; releases lease; `pr_url` required for worker, optional for tester)

**Tester**
- `report_tests {"task_id","passed":int,"failed":int,"log_excerpt?"}` → `{"task":Task}`. Requires an `in_progress` task of mine (409 otherwise).

**Worker feedback** (`request_help`, `suggest_split`: worker, tester, reviewer on tasks they can see; `report_blocker`: worker, tester). All three are rate limited by the loop guard like every non-read-only tool.
- `request_help {"task_id","kind":"unclear_spec|missing_access|conflicting_requirements|too_big|dependency_issue","details"}` → `{"request":HelpRequest}`. Posts a structured message to the task and brief threads and adds an owner `help` decision; the task status is unchanged. 409 for a `done` or `cancelled` task.
- `report_blocker {"task_id","reason","needs":"leader|owner"}` → `{"request":HelpRequest}`. Blocks the task exactly like `update_status` `blocked` (same checks: my `in_progress` task, `blocked_reason` `blocked by agent: <reason>`), records the structured need, posts to the task and brief threads and, for `needs` `leader`, to every active leader's agent thread.
- `suggest_split {"task_id","proposed_tasks":[{"ref?","title","spec","acceptance_criteria?","repo?","depends_on?","role?","budget?","priority?","component_id?"}]}` → `{"request":HelpRequest}`. 2 to 20 tasks; `repo` defaults to the task's repo; `depends_on` names a `ref` of another proposed task or an existing task of the brief that does not depend on the task being split; cycles are rejected. A new proposal supersedes the open one for the same task. Posts to the task and brief threads.

**Hill-climb** (worker and tester; listed only when `SUBSPACE_HILLCLIMB_WORKDIR` is set, see Hillclimb)
- `measure {"job_id","branch"}` → `{"attempt":{"id","job_id","agent_id","branch","value","status":"ok|failed","measured_at","log"},"metric","loss","best","job_status","stop_reason","remaining_attempts"}`. `loss` is the metric negated for `max` jobs; `remaining_attempts` is null for an unlimited budget; `metric` is the last number printed by `metric_cmd`. Requires an `in_progress` task of mine in the job's brief and repo whose branch is `branch` or a prefix of it followed by `-`, within task and brief budgets (`budget_exceeded`), and Subspace not paused; otherwise `not_assigned`. 409 `conflict` on a stopped job, `budget_exceeded` when the job has no attempts left, 403 for a repo outside `SUBSPACE_HILLCLIMB_REPOS`.

**Reviewer**
- `list_reviews {}` → `{"tasks":[Task]}` — `submitted`/`in_review` tasks where `reviewer` is me or unset, and the worker's provider differs from mine.
- `review {"task_id","verdict":"approve|changes_requested","comments"}` → `{"task":Task}`. approve → `approved`; changes_requested → `changes_requested` (`review_rounds`+1); at 3 rounds → `needs_owner`.

**Leader**
- `get_brief {"brief_id"}` / `list_briefs {"status?"}` → Brief(s)
- `list_help_requests {"status?":"open|accepted|superseded|closed|all","brief_id?","task_id?","limit?":1-1000}` → `{"requests":[HelpRequest]}` (help requests, blockers and split proposals, newest first; default `open`)
- `accept_split {"request_id"}` → `{"request":HelpRequest,"tasks":[Task],"original":Task,"rewired":["T-9"]}`. Creates the proposed tasks like `create_task` (ready or backlog), roots depend on the original's dependencies, tasks that depended on the original are re-pointed at the new tasks nothing else in the split depends on (`rewired`), then cancels the original. 409 if the proposal is not `open` or the task is `done`/`cancelled`. On a failure the created tasks are cancelled and the proposal stays open.
- `propose_plan {"brief_id","note?","tasks":[{"ref?","title","spec","acceptance_criteria":["..."],"repo","depends_on?":["<ref or T-id>"],"role?":"worker|tester","budget?":{"max_minutes","max_cost_eur"},"priority?":int,"component_id?":"C-1"}]}` → `{"plan":Plan}`. `ref` lets tasks depend on each other inside the plan. Supersedes any earlier proposed plan. The plan is linted first: findings with severity `error` (`dependency_cycle`, `unreachable_task`, `duplicate_ref`, `repo_outside_brief`, `spec_size` above 20000 characters) reject it with `bad_request` listing them; findings with severity `warning` (`acceptance_criteria`, `verification`, `spec_size`, `no_tester`, `duplicate_title`, `budget_missing`, `parallelism`) are stored in `Plan.warnings` and shown in the plan's decision summary.
- `create_task {"brief_id", ...task fields incl. "priority?", "component_id?"}` → `{"task":Task}` (status `ready` if the brief has an approved plan, else `backlog`)
- `update_task {"task_id","title?","spec?","acceptance_criteria?","depends_on?","budget?","status?":"blocked|ready|cancelled"}` → `{"task":Task}` (`ready` on a `blocked` task unblocks it: → `assigned` if it has an assignee, else `ready`; `backlog` tasks only become ready through owner plan approval). Tasks blocked for `budget_exceeded` or `poison_task` can only be unblocked by the owner (403, also for `reassign`). `budget` may only add or lower limits; raising or removing one is owner-only (403). `depends_on` follows the same rules as the owner PATCH (400 on unknown, self, cancelled, foreign-brief or cyclic dependencies); `create_task` checks `depends_on` the same way.
- `create_task_from_template {"brief_id","template_id","vars":{"<variable>":"<value>"},"repo","depends_on?":["T-1"],"priority?":int}` → `{"task":Task}`. Renders a task template (see Task templates) into the task's title, spec, `acceptance_criteria`, `role` and `budget`, then creates it like `create_task` (status `ready` if the brief has an approved plan, else `backlog`). 400 `bad_request` when a declared variable is missing or blank (`task template X: missing variables: a, b (declared: ...)`), a variable is not declared, a value exceeds 4000 bytes (16000 in total) or contains a control character other than newline and tab, or the rendered title is empty or longer than 200 characters or the spec longer than 20000; 404 for an unknown template, listing the available ids.
- `update_task {"task_id","title?","spec?","acceptance_criteria?","depends_on?","budget?","status?":"blocked|ready|cancelled"}` → `{"task":Task}` (`ready` on a `blocked` task unblocks it: → `assigned` if it has an assignee, else `ready`; `backlog` tasks only become ready through owner plan approval)
- `assign {"task_id","agent_id","as?":"assignee|reviewer"}` → `{"task":Task}` (`ready → assigned`; reviewer on `submitted` → `in_review`). Checks agent role, `active`, provider allowed by brief; reviewer provider ≠ worker provider.
- `reassign {"task_id","agent_id","reason?"}` → `{"task":Task}` (releases lease; → `assigned`)
- `board {"brief_id?"}` → `{"columns":{status:[Task]}}`
- `list_agents {"role?","status?"}` → `{"agents":[Agent]}`
- `capacity {}` → `{"global_max_agents":n,"limits":[CapacityLimit],"agents_in_use":n,"paused":bool}`
- `request_spawn {"brief_id","worker_kind":"claude-code|codex|opencode","model","n":1-50,"role?":"worker|tester","provider?"}` → `{"spawns":[Spawn]}` (`provider` follows the agent registration rules; `model` must match `[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}`, 400 otherwise); 409 `capacity` with the reason when capacity / max_parallel_agents / provider allow-list / budget / pause denies it.

### Handoff context
`get_task` and `claim_task` add a `context` object when the caller is the task's assignee, the task is `assigned`, `changes_requested` or `in_progress`, and it has a history: an earlier claim (for example after lease expiry or release), a reassignment, or at least one review round. Nobody else (leader, reviewer, owner) receives it, and it is absent on a task that has never been worked on.

```jsonc
// TaskContext
{"previous_assignees":["claude-1"],              // agents that claimed or were assigned the task before, oldest first, at most 10, never the caller
 "branch":"sub/t-3-add-login","pr_url":"https://...",   // pr_url null until submitted
 "tests":{"passed":7,"failed":2,"reported_at":"..."},   // or null
 "review_rounds":2,"review_comments":"...",             // comments of the latest changes_requested review, "" when none
 "messages":[{"id":42,"author":"claude-1","role":"worker","kind":"note","at":"...","text":"..."}],   // last 20 task thread messages
 "minutes_used":12,"cost_used":0.4,
 "truncated":false}                                     // true when messages were dropped or review comments shortened
```

Message texts are whitespace-collapsed, cut to 300 characters and redacted for tokens and secrets; `review_comments` is redacted and cut to 1500 characters. The serialized object never exceeds 8 KiB: when it would, the oldest messages are dropped first, then the review comments are shortened, and `truncated` is set.

### Threads scoping summary
Agent thread `agent:<id>`: leader messages to the agent, owner replies, answers, system notices. Task thread `task:<id>`: status changes (auto), notes, questions/answers, reviews, tests. Brief thread `brief:<id>`: plan decisions, leader notes.

