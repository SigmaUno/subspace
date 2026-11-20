# Subspace threat model

Scope: the Subspace server (REST, MCP, SSE), its web UI proxy, and the integrations described in [SPEC.md](../SPEC.md) and
[docs/api.md](api.md). "Control" means something the spec already states; "Gap" means the spec is silent, weak, or
explicitly defers it. Recommendations are proposals, not current behaviour.

## 1. Assets

| Asset | Why it matters | Where it lives |
|---|---|---|
| Owner token | Full authority: charter, budgets, agents, pause, mark done, audit | env / token file; web httpOnly cookie; CLI config |
| Agent tokens (`ssp_…`) | Act as one agent with that role's tools | Agent's MCP config (plaintext); server stores SHA-256 only |
| Charter + instructions | Appended to every agent's `get_instructions`; editing it changes all agents' behaviour | DB |
| Repos / branches / PRs | The actual product; Subspace stores no code but its tasks steer who changes what | Forgejo / GitHub |
| Task, brief, thread data | May contain proprietary specs, questions, test logs | SQLite DB, backups, SSE streams |
| Audit log | Evidence after an incident | DB (append-only by design) |
| Budget / provider quota | Money and rate limits at model providers | Provider accounts; `cost_used` in DB |
| Git host and provider credentials | Held by agents/spawner, not by Subspace | Agent machines, `hc` host |

## 2. Actors

| Actor | Trust | Capabilities |
|---|---|---|
| Owner | Fully trusted | Owner token via REST/CLI/UI; merges PRs |
| Honest agent | Trusted within role | One token, role-scoped MCP tools, git push to its own branch |
| Compromised / prompt-injected agent | Untrusted, holds a valid token and git credentials | Everything an honest agent can do, plus arbitrary text in tool arguments; may try to widen scope |
| Outside attacker | Untrusted | Network access to Caddy (`/mcp`, `/healthz`, web); may be able to open issues/PRs on the git host or serve web content that agents read |
| Malicious insider on the git host / provider | Out of scope for Subspace controls | Listed for completeness |

## 3. Trust boundaries

```
 owner browser ──► Caddy ──► Next.js web ──(owner token from httpOnly cookie)──► Go server /api/v1 (REST, SSE)
 agent process ──► Caddy ──► Go server /mcp (agent token)                          │
 agent process ──► git host (branch + PR)           Go server ──► spawner (hc / ssh) ──► agent machines
 agent process ──► model provider (prompts contain task text)
```

| Boundary | Crossing data | Existing control | Main concern |
|---|---|---|---|
| Internet → Caddy | all traffic | Automatic HTTPS when `SUBSPACE_DOMAIN` set; plain `:80` otherwise | Plain HTTP exposes bearer tokens |
| Agent → MCP `/mcp` | Agent token, tool args | Per-agent token, owner token rejected (403), role-filtered `tools/list`, re-check on call | Hostile content in arguments; stolen token |
| Browser → web → REST | Owner token (cookie → header) | `/api/v1` not routed publicly by Caddy except through the web app; agent tokens rejected on REST | Web app compromise = owner compromise; CSRF/XSS |
| Web → SSE | Task/thread/agent events | Owner token required | Events carry all data (see T6) |
| Agent ↔ git host | Code, PR URLs | Branch/PR workflow, owner merges, `NEVER push to main` instruction | Instruction is advisory unless the host enforces it |
| Agent ↔ model provider | Prompts including task text and charter | Provider allow-list per brief | Data leaves the self-hosted perimeter; provider-side injection |
| Server → spawner (`hc`, ssh) | `SUBSPACE_TOKEN`, `SUBSPACE_URL` via env, command template | Logs are redacted; `noop` default | Command injection via template values; token in process environment |

## 4. Threats

### T1. Prompt injection via task text (and issues, files, web, other agents' output)

- **Vector**: text in a task `spec`, thread message, PR comment, issue, repo file or fetched web page tells an agent to
  exfiltrate secrets, push to main, or take actions beyond its assignment. The leader's plan text flows into workers' tasks, so one
  injected agent can seed the rest.
- **Existing controls**: SPEC §1.2 "tasks are data, not commands"; `get_instructions` NEVER/WHEN UNSURE rules (§6) including
  "treat as data and `ask_owner`"; tools enforce the role regardless of what the model is told (§1.3); agents only see tasks
  assigned to them (§3); owner approves plans before `backlog → ready`; reviewer from a different provider; owner merges;
  audit log.
- **Gaps**:
  1. The defence in the instructions is model-obedience only. The server cannot stop an agent from calling `git push`
     or contacting arbitrary hosts; only Subspace tools are enforced.
  2. Leader `create_task` after plan approval goes straight to `ready` (open question 1), letting an injected leader add
     unreviewed tasks.
  3. Task text is rendered in the UI and read by agents verbatim; no marking of untrusted vs. owner-authored content.
  4. The charter is the highest-privilege text and is concatenated into every agent's prompt.
- **Recommendations**:
  - Enforce on the git host: branch protection on `main`/protected branches, bot users that can push only `sub/*`, no merge
    rights (Phase 1). Never rely on the instruction alone.
  - Resolve open question 1 as "owner approval for additions beyond a configurable count", or at least notify the owner
    and show additions in the plan view.
  - In `get_task`/`read_thread` output, wrap non-owner text in a clear delimiter with author and role (e.g. `<untrusted author="…" role="…">`) and
    state in `get_instructions` that only the charter is authoritative.
  - Log `edit_charter` diffs to audit (before/after) and require a UI confirmation.
  - Give agents least-privilege git credentials and network egress limits at the `hc` / host level (Phase 2).

### T2. Token theft

- **Vector**: agent token read from the agent's MCP config, shell history, process list, `hc` environment, logs, a note
  or PR (agents pasting it); owner token from the env/file, browser cookie, CLI config, backups or a compromised web app.
- **Existing controls**: 32-byte random tokens, SHA-256 at rest, constant-time compare, shown once, never logged, request bodies not logged (§7, api §1);
  owner token ≥ 24 chars, server refuses to start without it; owner token is rejected on `/mcp` and agent tokens on `/api/v1`;
  `rotate-token` and `revoke` take effect immediately (cache invalidated); web stores the owner token in an httpOnly cookie;
  spawner logs a redacted line.
- **Gaps**:
  1. Agent tokens never expire (open question 7).
  2. No rate limiting or lockout on failed auth is described (`rate_limited` exists as an error code but no policy).
  3. No source binding: a stolen token works from anywhere.
  4. Owner token is a single static, all-powerful secret with no rotation procedure or per-client scoping; the web cookie flags (`Secure`, `SameSite`) are unspecified.
  5. Tokens in plaintext in `SUBSPACE_TOKEN` env and in `claude mcp add --header` command lines (visible in `ps`/history).
  6. Secrets could leak into task/thread text, PRs or `log_excerpt`; instruction forbids it but nothing scans.
  7. Backups (`VACUUM INTO`) contain hashes, task data and the charter; permissions unspecified.
- **Recommendations**:
  - Add token expiry/rotation (e.g. 30-day TTL, `last_used_at` and `last_used_ip` shown in the agent view).
  - Throttle failed auth per IP and per token prefix; return the same 401 for unknown and revoked tokens.
  - Document owner-token rotation (restart with new token/file) and cookie flags: `HttpOnly; Secure; SameSite=Strict`.
  - Prefer config files or env-reference forms over inline CLI headers in connect snippets; document `chmod 600`.
  - Add a server-side secret scanner (token regex `ssp_[A-Za-z0-9_-]{43}`, common provider key prefixes) on message, note, `summary` and
    `log_excerpt` bodies that rejects or redacts and flags the agent.
  - Back up with mode 0600 into a directory outside any web root; document encrypting off-host copies.

### T3. Privilege escalation via tool calls

- **Vector**: an agent calls tools outside its role, reads/mutates other agents' tasks, sets forbidden statuses, reviews its own work, picks a provider that bypasses
  the allow-list, or leverages the leader to reach owner functions.
- **Existing controls**: role-filtered `tools/list` and re-check on every `tools/call`; owner-only actions are not on MCP at all (§3, api §5); data-level scoping
  (assignee/reviewer only; leader sees all); agents cannot set `submitted/approved/done`; reviewer provider ≠ worker provider; allowed providers
  enforced at assign and spawn; `Mcp-Session-Id` bound to the agent; owner token rejected on `/mcp`; every state change audited with before/after.
- **Gaps**:
  1. The leader is highly privileged (sees everything, assigns, spawns up to 50 agents per call, `update_task` status including `cancelled`);
     its compromise is the largest blast radius.
  2. `provider` is owner-declared per agent; the reviewer-differs rule is only as strong as that metadata. `register_agent(s)` is owner-only, which is good, but a mislabeled provider silently defeats it.
  3. `post_message` to another agent's thread (leader) is an injection channel (T1).
  4. A reviewer/tester sharing one git identity with a worker could push code; Subspace does not control git permissions.
  5. Reviewer `approve` has no link to the PR's actual head commit, so a worker can push after approval.
  6. `pr_url` is free text; nothing checks it matches the task's repo/branch.
- **Recommendations**:
  - Validate `pr_url` host and `repo` against the task and brief `repos`; store the head SHA at `submit` and `review`, and flag
    approval when the head differs at `done` time (UI warning to the owner).
  - Cap leader actions per brief (spawns, tasks created per time window), and alert the owner on bursts.
  - Use separate git identities per role; reviewer and tester get read-only (plus comment) access.
  - Test: a table-driven test that every owner-only tool is absent from every role's `tools/list` and rejected on direct `tools/call`, and that
    cross-task access returns `not_assigned`/`not_found` for each tool taking a `task_id` or `thread`.
  - Return `not_found` rather than `forbidden` for out-of-scope tasks, to avoid ID enumeration.

### T4. Loop and cost exhaustion

- **Vector**: runaway or malicious agent hammers tools, spins in claim/release cycles, reports low cost, spawns many agents, or an outside party floods `/mcp`.
- **Existing controls**: loop protection (same agent+tool+canonical args more than 5 times in 10 min → `paused`); per-task `max_minutes`/`max_cost_eur`,
  brief budget, `max_parallel_agents`, global max, per-provider capacity; lease expiry (15 min) and 5s sweeper; `pause_all`; `revoke`;
  `request_spawn` checks (n ≤ 50, brief open, budget not exceeded).
- **Gaps**:
  1. `cost_eur` is agent-reported (open question 6); an agent can under-report indefinitely. `minutes_used` only counts leased time.
  2. Loop detection keys on identical args, so varying one argument (e.g. a changing `note`) evades it. Read-only and heartbeat tools are exempt entirely.
  3. `requests_per_min` is informational; there is no per-token or per-IP request rate limit or body-size limit stated.
  4. Spawn requests are checked at request time, so retries across briefs and pending spawns can still exceed intent.
  5. Claim/release/reassign ping-pong is not specifically bounded.
  6. The 300-agent target means a single loop-pause per agent is a weak signal; there is no global kill when the aggregate request rate is anomalous.
  7. Provider spend happens outside Subspace (agents call providers directly), so the budget cannot hard-stop model usage.
- **Recommendations**:
  - Per-agent token-bucket rate limit on all MCP calls (including reads and heartbeat) and a max request body size (e.g. 256 KiB); return `rate_limited`.
  - Add a second loop heuristic on (agent, tool) regardless of args (e.g. > 60 state-changing calls per 10 min) and count
    claim→release cycles per task.
  - Use provider-side spending caps and per-agent API keys with hard monthly limits; treat Subspace `cost_eur` as advisory until Phase 3 usage tracking.
  - Cap total spawns per brief and per day, not just concurrent agents.
  - Add an owner alert (UI badge/SSE `control` event) when any agent is auto-paused or a budget block occurs.

### T5. Replay

- **Vector**: captured `Authorization` header or JSON-RPC message re-sent; a re-sent `tools/call` repeats a state change; `Mcp-Session-Id` reuse; SSE `Last-Event-ID` replay.
- **Existing controls**: HTTPS via Caddy when a domain is set; revoke/rotate kill tokens immediately; operations are state-machine checked (invalid transitions → 409), so duplicates of
  `claim_task`, `submit`, `review` mostly fail; session id is bound to the agent and optional; SSE has no guaranteed replay (clients refetch).
- **Gaps**:
  1. Bearer tokens are replayable by design; there is no nonce, timestamp or request signing.
  2. Non-idempotent tools without state guards (`add_note`, `post_message`, `ask_owner`, `create_task`, `request_spawn`, owner `POST …/messages`) duplicate on replay or client retry.
  3. HTTP `:80` mode (no `SUBSPACE_DOMAIN`) sends tokens in clear.
  4. Session ids are not required, so they add no protection.
- **Recommendations**:
  - Refuse to serve `/mcp` and `/api/v1` over plain HTTP unless the request comes from loopback or an explicit `SUBSPACE_ALLOW_INSECURE=1`;
    emit HSTS from Caddy.
  - Accept an optional `Idempotency-Key` / JSON-RPC id-based dedupe for create-style tools and spawns (short TTL cache).
  - Keep tokens short-lived (T2) to bound the replay window.

### T6. SSE data leak

- **Vector**: `/api/v1/events` with `topics=all` streams every task, thread message, question, agent and plan. A leaked owner token, a misconfigured proxy, a cache, or
  an overly broad CORS policy exposes everything; one agent subscribing would see all projects.
- **Existing controls**: owner token required; agent tokens get 403 on `/api/v1`; Caddy does not route `/api/v1` publicly except via the web app; the web app proxies it server-side
  so the owner token is not in the browser; topic filters; events have no replay.
- **Gaps**:
  1. A single owner-scoped stream carries all data; there is no per-topic authorization because there is only one principal. Any future multi-user or read-only viewer role needs scoping.
  2. The web proxy must not cache or buffer `text/event-stream` nor forward upstream auth headers/cookies to the browser; nothing specifies this.
  3. `message.created` payloads include full bodies, which may contain secrets that slipped in (T2).
  4. Long-lived connections are a resource-exhaustion vector (subscriber limit unspecified; 500+ tested for scale, not for abuse).
  5. Error and `lagged` handling must not leak other subscribers' data (low risk).
- **Recommendations**:
  - Send `Cache-Control: no-store` and `X-Accel-Buffering: no` on the stream; no `Access-Control-Allow-Origin` header on `/api/v1`.
  - Cap concurrent SSE subscribers per token and per IP; idle-timeout unauthenticated attempts.
  - If a read-only viewer is added later, give it its own token type with explicit topic allow-lists.
  - Keep events to identifiers plus the minimum fields the UI needs (thread bodies fetched on demand) if the sensitivity of threads grows.
  - Confirm that Caddy only exposes `/mcp` and `/healthz` directly, with a test in the deployment smoke test.

### T7. SQL injection

- **Vector**: task titles, specs, notes, agent ids, filters (`status`, `assignee`, `tool`, `since`, `limit`) and `thread` keys reaching SQL; the thread key and `kind` are parsed from paths and strings.
- **Existing controls**: all business logic goes through the `internal/store.Store` interface using plain SQL (§8); agent ids are validated by regex (`[a-z0-9][a-z0-9_-]{0,62}`);
  ids have a fixed shape (`T-<n>`); `limit` max 1000; pure-Go SQLite with a single writer.
- **Gaps**:
  1. The spec does not mandate parameterized queries, nor forbid string concatenation for dynamic filters/`ORDER BY`/`IN (…)` lists.
  2. Dynamic filter building (`/tasks?brief_id=&status=&assignee=&role=`, audit filters) is the usual injection spot.
  3. Free-text fields have no stated length limits.
  4. Stored content later rendered in the UI (XSS) is a sibling risk: briefs, specs and thread bodies are markdown.
  5. `VACUUM INTO` takes a path; if any part comes from a request it is injectable (the API takes `{}` only, which is fine).
- **Recommendations**:
  - Require `?` placeholders for every value; build dynamic `WHERE` from a fixed allow-list of column fragments; validate enums (`status`, `role`, `thread_kind`) and ids against
    their regex before reaching the store; reject unknown values with `bad_request`.
  - Add a lint/CI check (e.g. `grep`/`gosec` G201/G202) failing on `fmt.Sprintf` used with SQL strings in `internal/store`.
  - Add a store test with injection payloads (`' OR 1=1 --`, `'; DROP TABLE tasks;--`) in each filter and text field.
  - Set maximum lengths (title 200, spec/body/note 64 KiB, summary 16 KiB) and enforce in both REST and MCP.
  - In the web UI, render markdown with a sanitizer and no raw HTML; set a CSP at Caddy.
  - Open the read connection pool read-only so an injection in a read path cannot write.

### T8. Malicious PR

- **Vector**: an agent (or someone who can influence it) opens a PR containing a backdoor, CI changes that exfiltrate secrets, dependency confusion, workflow edits, or hides changes in a huge diff; the worker's PR is approved by a manipulated reviewer; an
  outside attacker's PR is mirrored in (Phase 1) as a task.
- **Existing controls**: work only lands via branch + PR (§1.4); no agent merges, the owner does (§1.5, §3); reviewer must be a different provider and checks against acceptance criteria; tester writes tests;
  `NEVER push to main`; 3 `changes_requested` rounds → `needs_owner`; audit log of submit/review.
- **Gaps**:
  1. Reviewer is an LLM and is subject to T1 via the PR content itself. Two models agreeing is not a security review.
  2. Nothing ties approval to a commit SHA (T3.5) so code can change after review.
  3. Changes to CI workflows, build scripts, dependency manifests and security-sensitive paths get no special handling; CI that runs on PRs may have access to secrets.
  4. No mention of signed commits, branch naming enforcement (`sub/<task-id>-<slug>`) at the host, or restricted PR targets.
  5. Phase 1 issue mirroring turns outside-attacker text into task text (T1).
  6. The owner may rubber-stamp after seeing "approved".
- **Recommendations**:
  - CI for agent PRs must run without secrets (e.g. `pull_request`, not `pull_request_target`; no deploy credentials on `sub/*`); require CI green before the UI enables `done`.
  - Record head SHA on `submit`/`review`; UI shows a warning if the head moved after approval, and a diff summary highlighting paths such as `.github/`, `.forgejo/`, `Makefile`, `Dockerfile`, lockfiles, `go.mod`, `package.json`,
    and anything under auth/permissions code.
  - Add a per-repo `sensitive_paths` setting; a PR touching them forces `needs_owner` rather than reviewer-only approval.
  - Enforce on the host: protected `main`, required review by the human owner, CODEOWNERS, bots limited to `sub/*`, force-push disabled, and
    optionally signed commits.
  - Phase 1: treat mirrored issues as untrusted content flagged as such, and require owner opt-in per issue before it becomes a task.

## 5. Other items worth tracking

- **Charter/brief/plan tampering**: owner-only on REST; audit the before/after text.
- **Spawner (`hc`)**: `SUBSPACE_HC_COMMAND` template with `{agent_id} {kind} {model} {join_line}` — agent ids are regex-limited, but `model` and `join_line` need shell-safe quoting
  (use argv, not a shell string) before Phase 2. Token goes via env, never argv.
- **Audit integrity**: append-only in code, but the DB file owner can edit it. Consider hash-chaining rows and shipping off-host.
- **Availability**: single-writer SQLite; large request bodies or slow writers can stall all agents (T4 limits help).
- **Deployment hygiene** (recommended; the spec only states distroless/static and Caddy routing): run the container as non-root, keep the data dir `0700`, and publish only Caddy's ports.

## 6. Reviewer checklist

Use this for any PR that touches auth, tools, store, web proxy, spawner or deployment.

**Auth and tokens**
- [ ] Agent tokens are never logged, returned after the first response, or stored in plaintext; comparison is constant-time.
- [ ] Owner token is rejected on `/mcp`; agent tokens are rejected on `/api/v1`; both have tests.
- [ ] Revoke/rotate invalidate the hot-path cache immediately.
- [ ] No request/response bodies or `Authorization` headers appear in logs, errors or panics.
- [ ] Cookies are `HttpOnly; Secure; SameSite`; no token in URLs or query strings (including SSE).

**Tools and permissions**
- [ ] New tool is in the matrix (SPEC §3, api §5), in role-filtered `tools/list`, and re-checked in `tools/call`.
- [ ] Owner-only action is not reachable through MCP.
- [ ] Data-level scoping is checked (assignee/reviewer; leader rules); out-of-scope returns without leaking existence.
- [ ] State changes go through the transition table; agents cannot reach `submitted/approved/done` except via the defined tools.
- [ ] Every state-changing path writes an audit row with before/after; no update/delete path to audit.
- [ ] Provider rules (allow-list, reviewer ≠ worker) are enforced on the server, not just stated in instructions.

**Input and storage**
- [ ] All SQL uses placeholders; dynamic fragments come from an allow-list; enums and ids validated.
- [ ] Length and size limits exist on every free-text field and request body.
- [ ] No SQLite-specific code outside `internal/store/sqlite`.
- [ ] Rendered markdown is sanitized; CSP is set.

**Limits**
- [ ] New tool is classified for loop protection (exempt only if truly read-only).
- [ ] Budgets, leases and `pause_all` still stop the new behaviour (claims, spawns, background loops).
- [ ] Per-token rate limit applies; failure returns `rate_limited`.

**SSE and web**
- [ ] New event types carry only data the owner is meant to see; topic mapping is documented in api §4.
- [ ] Stream responses are `no-store`, unbuffered, with subscriber caps.
- [ ] `/api/v1` remains unrouted publicly except through the web app (check Caddyfile).

**Agent-facing text and git**
- [ ] `get_instructions` NEVER/ALWAYS rules are unchanged or strengthened; the charter is last.
- [ ] Task/thread text returned to agents is treated as data (delimited, attributed).
- [ ] Nothing in the change lets an agent push to protected branches, merge, or mark `done`.
- [ ] PR/CI changes: no secrets exposed to `sub/*` PR builds; sensitive-path changes flagged.

**Spawner and deployment**
- [ ] Spawner passes values via argv/env, not shell interpolation; token never on the command line or in logs.
- [ ] Plain-HTTP mode is documented as local-only; docs and snippets use `https://`.
- [ ] Backups are 0600 and excluded from any served path.
- [ ] Docs updated in the same PR (SPEC.md / docs/api.md / this file) if behaviour changed.
