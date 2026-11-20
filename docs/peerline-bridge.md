# Peerline bridge (design, Phase 3)

Status: **design only**. No code, no API change. Implementing it requires updating [api.md](api.md) (owner endpoints, §3) and SPEC §8 config in the same PR.

## 0. Goal and non-goals

Subspace is a task and governance layer. The Peerline bridge lets a Subspace instance expose a **read-only status summary** to bots in other accounts (for example an owner's personal Grok Bot) without giving them access to the board, agents, tokens, or message channels.

Non-goals:
- No inbound control. Remote bots cannot create briefs, tasks, messages to agents, or change any state.
- No memory or workspace sync. Subspace never sends task text, specs, notes, questions, code, or logs.
- Agents never talk to Peerline. No agent role gets a Peerline key or an MCP tool for it. Only the Subspace server process holds the Peerline key.

## 1. Reference: Peerline

[Peerline](https://github.com/sysrex/peerline) is a small Go relay for cross-account bot messaging. The parts this design relies on:

- **Peers**: each side registers and gets a `pl_` API key (shown once, SHA-256 stored).
- **Pairing**: directional grants (`messages`, `bags:read`, `bags:write`) plus a bag-name allowlist. Default is deny-all. Confirm-mode invites (`require_confirm`) need an explicit confirmation by the inviter.
- **Bags**: named blobs (`^[a-z0-9][a-z0-9._-]{0,63}$`), at most 64 KiB each, with a per-owner quota (default 100 bags / 1 MiB). `PUT/GET /v1/bags/{name}` with ETag `"vN"`.
- **Messages**: at most 4 KiB, pulled from `GET /v1/inbox`. Email wake-ups are best-effort.
- **Limits**: write bucket default 10/min, burst 3. Bag PUT has its own per-caller bucket (30/min, burst 10). Over budget → 429 with `Retry-After`.

Peerline has no server-to-client push. The only push is the email wake-up, which carries no state. So the bridge is **publish-to-bag, pull-by-reader**.

## 2. What is exposed

The bridge publishes one bag, default name `subspace-status`, containing a JSON document of at most 8 KiB.

```jsonc
{
  "schema": 1,
  "generated_at": "2026-11-20T21:04:05Z",   // server time of snapshot
  "instance": "subspace-<short id>",       // random per install, not the URL
  "paused": false,
  "pause_reason": null,
  "briefs": [                                // open briefs only, by opaque id
    {
      "id": "B-1",
      "status": "open",
      "plan_status": "approved",
      "progress": {"done": 4, "total": 11},  // done = done; total excludes cancelled
      "tasks": {"ready": 2, "in_progress": 3, "submitted": 1, "in_review": 1, "blocked": 0,
                "needs_owner": 0, "changes_requested": 0, "approved": 0, "done": 4}
    }
  ],
  "agents": {"active": 9, "paused": 1, "revoked": 0},   // counts only, no ids
  "open_questions": 2,                       // count only
  "proposed_plans": 1                        // count only
}
```

Rules:
- Counts and status enums only, plus the opaque brief id. Nothing free-form.
- Brief **titles are excluded by default**. They are owner text and could identify work. A later config flag can add them.
- Costs (`cost_used`, `budget`) are **excluded by default**. A flag `SUBSPACE_PEERLINE_INCLUDE_COSTS` can add `cost_used` per brief.
- Never included: task titles and specs, acceptance criteria, repo names, branches, PR URLs, notes, messages, question text, agent ids, tokens, charter text, audit data, or capacity config.
- A snapshot is only written when the read is consistent (one read transaction).

Closed briefs are dropped when they go closed. Disabling the bridge overwrites the bag with `{"schema":1,"enabled":false,"generated_at":"..."}` because Peerline has no bag delete API.

## 3. Push vs pull

- **Subspace → Peerline: push to bag.** The bridge publishes after state changes, coalesced. It runs as one goroutine with a dirty flag. Relevant events come from the internal event hub (`board`, `control`, `plans`, `questions`, `agents`). Any number of events in one window produce one publish.
- **Interval**: publish at most once per `SUBSPACE_PEERLINE_INTERVAL` (default `60s`, minimum `30s`). A periodic publish runs every interval even with no changes, so readers can tell the bridge is alive.
- **Reader → Peerline → Subspace: pull by reader only.** Remote bots call `GET /v1/bags/subspace-status?owner=<subspace peer id>` with their own key. Subspace never polls them.
- **Messages are not used.** Subspace does not grant the `messages` scope and does not read its inbox. Anything that arrives in the Subspace inbox is discarded (and logged as a count only). Reason: an inbound channel would be a new instruction source, which SPEC principle 2 forbids.
- Optional alerts (for example "paused") could later be sent as a message by Subspace. That is out of scope for this design and would need its own review.

Readers treat the bag as stale if `generated_at` is older than 15 minutes (`SUBSPACE_PEERLINE_STALE_AFTER`, informational). The periodic publish keeps `generated_at` fresh even when nothing changed, so an old timestamp means Subspace is down or the bridge is failing.

## 4. Auth and pairing

- **Key**: one Peerline peer for the Subspace instance, registered by the owner. Its `pl_` key is loaded only from a file (`SUBSPACE_PEERLINE_KEY_FILE`), never from a plain env var, and never logged. It is not part of any join line, snippet, or API response. Only the process reads it. Permissions on the file: 0600.
- **Grant to reader**: the owner creates the invite from the Subspace side with:
  - scopes `["bags:read"]` only
  - bags `["subspace-status"]` only
  - `require_confirm: true`, so the reader's claim shows an offer digest that the owner checks before confirming
  - a short `ttl_seconds` (e.g. 3600)
- Subspace does not grant `bags:write` to anyone, and does not grant `messages`.
- The reader's reciprocal grant is not requested. If the reader offers any scope back, Subspace ignores it for the bridge (it does not read their inbox or bags).
- **Revoke**: the owner revokes with `DELETE /v1/grants/{reader_peer_id}`. Access ends immediately. The owner can also rotate Subspace's key (`POST /v1/me/rotate-key`), which kills all readers' access to writes and requires updating the key file.
- Pairing URLs are not used for this flow. They are for agents without an inbox, and this grant should be confirmed by the owner, not by a link.

Owner-facing API (proposed, owner token only, never MCP):

| Method | Path | Meaning |
|---|---|---|
| GET | `/api/v1/peerline` | config state: enabled, peer id, bag name, last publish time, last error (no key) |
| POST | `/api/v1/peerline/invites` | create confirm-mode invite as above; returns Peerline invite public_id and pairing info |
| POST | `/api/v1/peerline/publish` | force a publish now (rate-limited to 1 per 30s) |

Those are design proposals and go into api.md when implemented.

## 5. Rate limits

- Subspace publishes at most once per interval (default 60 s). Peerline's default write bucket is 10/min with burst 3, and bag PUT is 30/min burst 10, so the default is well under both.
- The bridge keeps its own token bucket, sized to the Peerline limits it sees, so a misconfigured interval cannot exceed them.
- Forced publishes are limited to one per 30 s. Beyond that the call returns `rate_limited` (429, api.md §1).
- Reader-side limits are the reader's concern. The bridge's bag is cacheable.
- Size: the bag is at most 8 KiB. If the document exceeds it, the bridge drops the `briefs` array to the first N open briefs (ordered by `created_at`) and sets `"truncated": true`. It never writes a partial document.

## 6. Failure modes

| Failure | Detection | Behaviour |
|---|---|---|
| Peerline unreachable / 5xx | timeout or 5xx on PUT | exponential backoff 30 s → 15 min, keep last good bag, log once per streak |
| 429 | status 429 | honor `Retry-After`, but never less than the backoff step (Peerline's Retry-After 2 is shorter than the real refill) |
| 401 (key invalid or rotated) | status 401 | stop publishing, set `last_error=auth`, do not retry more than once per hour; owner must update the key file |
| 403 | status 403 | treat as "not allowed to write": stop, set `last_error=forbidden`, owner action required |
| 413 (quota / size) | status 413 | shrink document (drop briefs, then counts-only), retry once; if still 413, stop and alert the owner |
| 412 / ETag mismatch | status 412 | refetch ETag and retry; the bridge is the only writer, so this indicates a second instance: refuse to start on that case |
| Subspace down | reader sees old `generated_at` | readers treat as stale after 15 min; no action from Subspace |
| Clock skew | reader compares `generated_at` | readers should use a generous window; generated_at is server time, not per-reader |
| Key leaked | owner notices | rotate Peerline key, update file, restart; old key dead immediately |
| Reader key compromised | owner notices | revoke that grant; the bag remains readable only by remaining grants |
| Owner quota full | Peerline 413 on new bag | the bag is the only one the bridge writes; quota full cannot happen unless the owner stores other bags on the same peer. Use a dedicated peer for Subspace to avoid that |
| Partial write | ETag / body | Peerline either stores the whole body or fails; the bridge never sends partial JSON |
| Bridge paused by Subspace `pause_all` | control event | keep publishing with `"paused": true` so readers see the state; no agent-visible change |
| Disabled | config | tombstone document (see §2), then stop |

Logs: publish result, status code, size, ETag. Never the key, invite codes, pairing tokens, or response bodies from Peerline.

## 7. Implementation sketch

Package `internal/peerline` (new). It depends on interfaces only, so the store stays SQLite-free (SPEC §8).

```go
package peerline

// Snapshot is the document in §2. Built from one read transaction.
type Snapshot struct {
    Schema        int          `json:"schema"`
    GeneratedAt   time.Time    `json:"generated_at"`
    Instance      string       `json:"instance"`
    Paused        bool         `json:"paused"`
    PauseReason   *string      `json:"pause_reason"`
    Briefs        []BriefStatus `json:"briefs"`
    Agents        AgentCounts  `json:"agents"`
    OpenQuestions int          `json:"open_questions"`
    ProposedPlans int          `json:"proposed_plans"`
    Truncated     bool         `json:"truncated,omitempty"`
}

// SnapshotSource reads the status counts (implemented on the store read pool).
type SnapshotSource interface {
    Snapshot(ctx context.Context, opts SnapshotOptions) (Snapshot, error)
}

// BagWriter is the Peerline HTTP client surface the bridge needs.
type BagWriter interface {
    PutBag(ctx context.Context, name string, body []byte, ifMatch string) (etag string, err error)
}

// Errors the bridge classifies for backoff and owner alerts.
var (
    ErrUnauthorized = errors.New("peerline: unauthorized")
    ErrForbidden    = errors.New("peerline: forbidden")
    ErrRateLimited  = errors.New("peerline: rate limited")
    ErrTooLarge     = errors.New("peerline: too large")
    ErrPrecondition = errors.New("peerline: precondition failed")
)

// Bridge publishes snapshots. One goroutine. Dirty events are coalesced.
type Bridge interface {
    Run(ctx context.Context) error // blocks until ctx is done
    Trigger()                      // mark dirty (non-blocking)
    Status() Status                // for GET /api/v1/peerline, no secrets
}
```

Wiring in `cmd/subspace`:
- The bridge subscribes to the internal event hub for `board`, `control`, `plans`, `questions`, `agents` topics and calls `Trigger()`.
- A timer forces a publish every interval.
- `Run` returns `nil` and stays idle if the bridge is disabled (`SUBSPACE_PEERLINE_URL` empty).

Testing:
- Table tests for the document builder: no titles, specs, repo names, costs (unless flagged), no agent ids.
- Fake `BagWriter` covering 401, 403, 413, 429, 5xx, and 412 with the expected backoff and alert transitions.
- Coalescing test: 500 triggers in one window → one publish.
- Size test: a document with 500 open briefs stays ≤ 8 KiB and sets `truncated`.
- Secret test: the key never appears in logs or in `Status()` output.

## 8. Configuration (env)

| Var | Default | Meaning |
|---|---|---|
| `SUBSPACE_PEERLINE_URL` | – | Peerline base URL. Unset = bridge off |
| `SUBSPACE_PEERLINE_KEY_FILE` | – | file with the `pl_` key (0600). Required when URL is set. No env var for the key |
| `SUBSPACE_PEERLINE_BAG` | `subspace-status` | bag name, must match Peerline bag rules |
| `SUBSPACE_PEERLINE_INTERVAL` | `60s` | periodic publish interval (min `30s`) |
| `SUBSPACE_PEERLINE_STALE_AFTER` | `15m` | informational; written into the document so readers can apply it |
| `SUBSPACE_PEERLINE_INCLUDE_COSTS` | `false` | include per-brief `cost_used` |
| `SUBSPACE_PEERLINE_MAX_BRIEFS` | `20` | cap on briefs in the document before truncation |

Startup validation: URL must be `https://` unless it is a loopback address; key file must exist and not be world-readable; bag name must match the Peerline regex. Failures refuse startup of the bridge (not of Subspace), and log one line with no secret values.

## 9. Open questions

1. Should the Subspace peer be dedicated to the bridge, so that Peerline bag quota and write limits are not shared with other uses? (Recommended: yes.)
2. Should brief titles be an opt-in flag, or stay out of the bridge entirely? (Recommended: stay out until the owner asks.)
3. Peerline's bag PUT content type and whether `owner=` is needed on writes: verify against the pinned Peerline version before implementation.
4. Does the owner want a second bag with per-brief detail, or only the summary? (Current design: summary only.)
5. Stale threshold: is 15 minutes right for a personal reader, given the default 60 s interval?
