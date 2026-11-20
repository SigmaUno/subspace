# Subspace operator guide

Audience: the person who runs Subspace on a Linux server with Docker. Subspace is the task and governance layer (see
[SPEC.md](../SPEC.md)); the API contract is in [api.md](api.md). Everything here is based on those two documents.

**Status legend.** Items marked **planned** are specified but not yet on `main` (the Go server, Dockerfile, compose file,
Caddyfile, `subspacectl` and the web UI are on other branches or not yet written). Do not rely on a planned item until it
has landed and been verified on your server.

| Item | Status |
|---|---|
| REST `/api/v1`, MCP `/mcp`, `/healthz`, audit, backups (`VACUUM INTO`) | specified in api.md / SPEC §7–§8, server **planned** on `main` |
| Docker image, `docker-compose` stack, Caddy (HTTPS via `SUBSPACE_DOMAIN`) | **planned** (SPEC §12) |
| `subspacectl` owner CLI | **planned** (SPEC §13 Phase 0) |
| Web UI (`web/`) | **planned** (placeholder on `main`) |
| Automatic daily backup, `pause_all` kill switch | specified (SPEC §7, §10) |

---

## 1. Layout on the host

Data lives under `${SUBSPACE_DATA:-/data/subspace}` (SPEC §12). The layout below is the **planned** layout; the compose file
that creates it is not on `main` yet.

```
/data/subspace/
├── subspace.db          SQLite database (WAL mode), plus subspace.db-wal and subspace.db-shm while running
├── backups/             VACUUM INTO snapshots, subspace-<UTC timestamp>.db, kept SUBSPACE_BACKUP_KEEP (default 14)
├── caddy/               Caddy data (certificates, ACME state) and config
└── secrets/
    └── owner-token      owner bearer token, one line, mode 0600
```

Rules:

- Do not put the `.db` file in a directory that is served or synced publicly.
- `secrets/` is readable by the operator and the server container only: `chmod 700 /data/subspace/secrets`,
  `chmod 600 /data/subspace/secrets/owner-token`.
- The default `SUBSPACE_DB` in SPEC §10 is `/data/subspace.db`. Set `SUBSPACE_DB=/data/subspace/subspace.db` explicitly so
  the database, backups and WAL files stay together. Backups default to `<db dir>/backups`, i.e. `/data/subspace/backups`.

## 2. Secrets

| Secret | Where | Notes |
|---|---|---|
| Owner token | `/data/subspace/secrets/owner-token` | ≥ 24 characters. Read via `SUBSPACE_OWNER_TOKEN_FILE`. If the file is set it wins over `SUBSPACE_OWNER_TOKEN`. The server refuses to start if neither is set. |
| Agent tokens | shown once on register / rotate; stored by the agent's own runtime | Server keeps only SHA-256 hashes. Lost token = rotate it. |
| Caddy / TLS | `/data/subspace/caddy/` | Managed by Caddy (automatic HTTPS when `SUBSPACE_DOMAIN` is set). |

Create the owner token (48 hex characters, well above the minimum):

```sh
install -d -m 700 /data/subspace/secrets
umask 077
openssl rand -hex 24 > /data/subspace/secrets/owner-token
```

Never use the development default from the `Makefile` (`dev-owner-token`) on a server. Never put the token in compose
files committed to git, in `.env` files in the repo, in shell history, in task notes, commits or PRs.

To use the token in your own shell without echoing it:

```sh
export SUBSPACE_OWNER_TOKEN="$(cat /data/subspace/secrets/owner-token)"
# Base URL of the Go server as reachable from the server host (planned: from the compose network). Not the public Caddy URL,
# because Caddy does not route /api/v1 publicly.
export SUBSPACE_API="http://<internal-server-address>:8080"
```

## 3. First deploy

Status: **planned** (needs the image, compose file and Caddyfile from SPEC §12).

1. Install Docker Engine and the compose plugin. Create the host layout from §1 and the owner token from §2.
2. Set the environment in an env file **outside** git (for example `/data/subspace/subspace.env`, mode 0600):
   - `SUBSPACE_PUBLIC_URL=https://<your-domain>` (used in join lines and connect snippets; must be the URL agents reach)
   - `SUBSPACE_DOMAIN=<your-domain>` (enables automatic HTTPS in Caddy; leave unset for plain `:80`)
   - `SUBSPACE_OWNER_TOKEN_FILE=/run/secrets/owner-token` (mount `secrets/` read-only into the server container)
   - `SUBSPACE_DB=/data/subspace/subspace.db`
   - optional: `SUBSPACE_LEASE` (default `15m`), `SUBSPACE_SPAWNER` (`noop` by default), `SUBSPACE_LOG_LEVEL` (`info`)
3. Start the stack (planned compose file): `docker compose --env-file /data/subspace/subspace.env up -d`.
4. Check health (see §10): `curl -fsS https://<your-domain>/healthz` must return `{"status":"ok",...}`.
5. Check the owner API with the token (must return JSON, not 401/403):
   ```sh
   curl -fsS -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" $SUBSPACE_API/api/v1/state
   ```
6. Bootstrap: with an empty DB nothing breaks; `/mcp` rejects all tokens until agents are registered. Create the first brief
   and register agents (§5).

### Setting the domain and HTTPS

- `SUBSPACE_DOMAIN` set: Caddy obtains and renews the certificate automatically. DNS for the domain must point at the server,
  and ports 80 and 443 must be reachable from the internet (ACME needs them).
- `SUBSPACE_DOMAIN` unset: Caddy serves plain HTTP on `:80`. Use only on a private network or behind another TLS proxy.
- Caddy routes `/mcp` and `/healthz` to the Go server and everything else to the web app. The web app proxies `/api/v1`
  server-side with the owner token from an httpOnly cookie (SPEC §12). Caddy must not route `/api/v1` publicly. The REST
  examples in this guide assume you run them from the server host, against the Go server on the internal compose network
  (planned: exact address once the compose file exists).
- Agents reach `https://<your-domain>/mcp` with `Authorization: Bearer <agent token>`.

## 4. Owner UI and owner CLI

- The web UI (planned) logs in with the owner token and keeps it in an httpOnly cookie.
- `subspacectl` (planned) is the owner CLI. Until it lands, every owner action in this guide is a REST call (api.md §3)
  with the owner bearer token. Command names will be documented here once `subspacectl` exists; do not guess them.

## 5. Registering agents

Status: REST endpoints are specified (api.md §3, Agents); `subspacectl` wrapper **planned**.

Register one agent (returns the token once):

```sh
curl -fsS -X POST -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"claude-1","role":"worker","kind":"claude-code","model":"claude-sonnet-5-5"}' \
  $SUBSPACE_API/api/v1/agents
```

- `kind` is one of `claude-code` (provider `anthropic`), `codex` (`openai`), `opencode` (`deepseek`), or `other`. `provider`
  defaults from `kind`.
- Ids match `[a-z0-9][a-z0-9_-]{0,62}`.
- The response contains `connect.token` (the plaintext token) and ready-made snippets. Give the token to the agent's runtime
  through its own config (see SPEC §9 for Claude Code, Codex and OpenCode snippets). Do not paste it into Subspace threads,
  tasks or git.
- Bulk: `POST /api/v1/agents/bulk` with `{"prefix":"claude","count":50,...}` creates `claude-001`…`claude-050` atomically
  (max 1000).
- Re-show the connect snippet without a token: `GET /api/v1/agents/{id}/connect`.
- Allowed providers are set per brief (`allowed_providers`); the leader can only assign agents whose provider is allowed.

Revoke an agent (token dies immediately, leases are released):

```sh
curl -fsS -X POST -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" -H "Content-Type: application/json" \
  -d '{"reason":"left the project"}' $SUBSPACE_API/api/v1/agents/claude-1/revoke
```

Resume an agent paused by loop protection: `POST /api/v1/agents/{id}/resume`. Investigate the loop first (system message in
the agent thread) before resuming.

## 6. Owner token rotation

Status: the procedure is specified as a restart-based change; **verify once the server is on `main`**, because no rotation
endpoint is defined in api.md.

1. Generate a new token into a temporary file in the same directory (`umask 077`), never into the repo.
2. Replace `/data/subspace/secrets/owner-token` with the new value (the file wins over the env var).
3. Restart the server container: `docker compose restart subspace-server` (planned service name).
4. Update your local shell (`export SUBSPACE_OWNER_TOKEN=...`) and any saved scripts. Remove the old value from notes.
5. Confirm: an old-token call returns `401`, the new one returns `200` on `GET /api/v1/state`.

## 7. Agent token rotation and compromise

- Routine rotation: `POST /api/v1/agents/{id}/rotate-token` with `{}`. Response has the new token once; the old token is dead
  immediately. Deliver it to the agent's runtime; the snippets in `connect` contain the new token.
- Suspected leak of an agent token: rotate it at once (the leaked token stops working). Then check the audit log for that
  agent: `GET /api/v1/audit?agent_id=claude-1&since=<id>`. If the agent is not needed, revoke it instead.
- Never rotate the owner token and agent tokens in one step without checking that the owner token still works.
- Token expiry is not implemented; tokens do not expire until rotated or revoked (SPEC §14, open question 7).

## 8. Kill switch: `pause_all`

```sh
curl -fsS -X POST -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" -H "Content-Type: application/json" \
  -d '{"reason":"runaway spend"}' $SUBSPACE_API/api/v1/pause-all
```

Effects (api.md §3, SPEC §7): global pause flag; all leases revoked (`in_progress` → `assigned`); claims and spawns are blocked;
agents' `heartbeat` returns `paused:true`; `control.paused` is published on the SSE `control` topic.

Resume: `POST /api/v1/resume-all` with `{}`. Agents re-claim their tasks.

Notes:

- `pause_all` does **not** stop agent processes. Agents keep running on their machines but cannot claim work or spawn. To stop
  processes, stop them on their hosts (or via `hc` once Phase 2 spawning is in use — planned).
- Use `GET /api/v1/state` to confirm `"paused":true` after pausing.
- For a single runaway agent, revoke or rotate its token instead of pausing everything.

## 9. Backup and restore (SQLite)

### Automatic
- Default: daily (`SUBSPACE_BACKUP_INTERVAL=24h`; `0` disables), into `SUBSPACE_BACKUP_DIR` (default `<db dir>/backups`),
  retaining `SUBSPACE_BACKUP_KEEP=14` files. Implemented with `VACUUM INTO`, which produces a consistent copy while the server runs.
- Status: specified (SPEC §7/§10); verify that files appear in `/data/subspace/backups` after the first day.

### On demand
```sh
curl -fsS -X POST -H "Authorization: Bearer $SUBSPACE_OWNER_TOKEN" -H "Content-Type: application/json" -d '{}' \
  $SUBSPACE_API/api/v1/backup
# {"path":"/data/subspace/backups/subspace-20261120T210405Z.db","bytes":12345}
```

### Off-host copy (operator responsibility)
Backups on the same disk do not protect against disk loss. Copy `/data/subspace/backups/` to another machine or storage on
a schedule of your choice. Subspace does not do this for you; SPEC does not specify an off-host backup.

### Check a backup
Use the SQLite CLI on a copy (not on the live file):
```sh
cp /data/subspace/backups/<file>.db /tmp/opencode/check.db
sqlite3 /tmp/opencode/check.db "PRAGMA integrity_check;"    # expect: ok
rm /tmp/opencode/check.db
```

### Restore (manual, untested until the server is on `main`)
There is no restore endpoint. Restore replaces the live database, so it causes downtime and loses any writes made after the
backup was taken.
1. Note the time, run `pause_all` (§8) so agents stop writing, then stop the server: `docker compose stop subspace-server`.
2. Keep the current state: `mv /data/subspace/subspace.db /data/subspace/subspace.db.pre-restore` and move `-wal` and `-shm`
   files aside too (`subspace.db-wal`, `subspace.db-shm`). Do not delete them yet.
3. Copy the chosen backup to `/data/subspace/subspace.db` (ownership as the server user).
4. Start the server: `docker compose start subspace-server`. Check `/healthz` and `GET /api/v1/state`.
5. Resume agents with `resume-all` only after you have checked the restored board. Agents may need to re-claim tasks.
6. Keep the `.pre-restore` files until you are satisfied; then remove them manually.

Record the restore in the brief thread (`POST /api/v1/threads/brief/<id>/messages`) so the audit trail explains the gap.

## 10. Monitoring and health

- Health: `GET /healthz` (no auth) returns `{"status":"ok","version":"...","paused":false}`.
- Minimum checks (any uptime monitor or cron):
  - HTTP status of `https://<your-domain>/healthz` is `200` and `status` is `ok`.
  - `paused` is `false` unless you paused on purpose. Alert if it changes unexpectedly.
  - Backup freshness: newest file in `/data/subspace/backups/` is less than 25 hours old.
  - Disk usage of `/data/subspace` (the DB grows with audit and messages).
- Owner view: `GET /api/v1/state` gives counts (agents, task statuses, open questions, proposed plans). Use it for dashboards
  if needed. Keep the token out of monitoring UIs that others can read.
- SSE stream `GET /api/v1/events` sends `: ping` every 15s; a missing ping means the stream or server is down.

## 11. Logs

- Server logs go to stdout/stderr at `SUBSPACE_LOG_LEVEL` (default `info`, slog). With compose: `docker compose logs -f subspace-server` (planned service name). Use `--since` to narrow.
- Request bodies are not logged; tokens are never logged (SPEC §7). Spawner lines are redacted.
- Caddy access and error logs: **planned**; expect them in the Caddy container's logs (`docker compose logs caddy`).
- Audit log is not a log file: it is in the database and read via `GET /api/v1/audit?agent_id=&task_id=&tool=&since=&limit=`
  (append-only, no delete path). Export it if you need an offline record.
- Docker's own log rotation (`json-file` driver, `max-size`/`max-file`) must be configured by you; planned in the compose file.

## 12. Firewall

Recommended host policy (`ufw`):

```sh
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp     # SSH (consider restricting to your IP and disabling password login)
ufw allow 80/tcp     # Caddy: ACME challenge and HTTP
ufw allow 443/tcp    # Caddy: HTTPS (/mcp, /healthz, web UI)
ufw enable
```

Important: **Docker-published ports bypass `ufw`.** Docker writes its own iptables rules, so a container port published with
`-p 8080:8080` is reachable from the internet even though `ufw` says deny. Therefore:

- Publish ports only on the Caddy service (80 and 443). The Go server and the web app must not have a `ports:` entry; they
  reach Caddy over the internal compose network only.
- Do not run `subspace-server` with `SUBSPACE_ADDR` published on the host. If you must expose a port for debugging, bind it to
  `127.0.0.1` in the compose file, and remove it afterwards.
- After any compose change, verify from another machine: `nmap -Pn -p 8080,3000 <server-ip>` should show closed/filtered.
- Outbound: agents connect through the public URL; the server itself needs outbound HTTPS only if the Phase 1 forge integration
  is enabled (planned).

## 13. Upgrade and rollback

Status: planned (image tags and compose file not on `main`). The steps are the intended procedure.

Upgrade:
1. Read the release notes and the diff of `docs/api.md`. If the API contract changed, check that your agents and `subspacectl` match.
2. Take a backup: `POST /api/v1/backup` (§9). Copy it off the host.
3. Optionally `pause_all` (§8) to stop claims during the upgrade.
4. Pull and start the new tag: set the image tag in the env file, then `docker compose pull && docker compose up -d`.
5. Verify: `/healthz` returns the new `version`; `GET /api/v1/state` works; one `GET /api/v1/tasks` returns data.
6. `resume-all` if you paused.

Rollback:
1. `pause_all`.
2. Set the previous image tag and `docker compose up -d`.
3. If the new version ran database migrations, the old binary may not read the new schema. The migration policy is not defined
   yet (planned). If in doubt, stop the server, restore the pre-upgrade backup (§9), then start the old version.
4. Verify health and state, then `resume-all`.

Do not roll back by editing the database by hand.

## 14. Routine checklist

- Daily: `/healthz` ok; backup file created; `paused` is as expected.
- Weekly: off-host copy of backups verified; disk space; review `GET /api/v1/questions?status=open` (agents waiting for the owner).
- After any token leak or agent departure: rotate/revoke (§5, §7), check audit.
- Before any merge the owner does: nothing here merges; merging is the owner's action in git, then `POST /api/v1/tasks/{id}/done`.
