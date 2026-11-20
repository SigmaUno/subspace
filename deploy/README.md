# Deploy Subspace with Docker Compose

Run these steps on the Linux host from a checkout that includes the Go server and the web app. Use Docker Engine with the Compose plugin. Caddy is the only service that publishes host ports.

## Prepare persistent storage

Create the host directories. The server and backup images run as UID and GID 65532.

```sh
export SUBSPACE_DATA=/data/subspace
sudo install -d -m 0755 "$SUBSPACE_DATA"
sudo install -d -m 0700 -o 65532 -g 65532 "$SUBSPACE_DATA/db" "$SUBSPACE_DATA/backups"
sudo install -d -m 0750 -o root -g 65532 "$SUBSPACE_DATA/config"
sudo install -d -m 0700 "$SUBSPACE_DATA/caddy/data" "$SUBSPACE_DATA/caddy/config"
cp .env.example .env
chmod 600 .env
```

Generate an owner token and set `SUBSPACE_OWNER_TOKEN` in `.env`, or install a token file in the config directory and set `SUBSPACE_OWNER_TOKEN_FILE=/data/config/owner.token`. The file must be readable by UID 65532. The token needs at least 24 characters.

```sh
openssl rand -hex 32 | sudo install -m 0400 -o 65532 -g 65532 /dev/stdin "$SUBSPACE_DATA/config/owner.token"
```

Set the following in `.env` before starting the stack.

| Setting | Domain deployment | IP deployment |
|---|---|---|
| `SUBSPACE_DOMAIN` | `subspace.example.com` | Leave empty |
| `SUBSPACE_PUBLIC_URL` | `https://subspace.example.com` | `http://192.0.2.10` |

For HTTPS, point DNS at the host and allow inbound TCP 80 and 443 so Caddy can issue and renew its certificate. UDP 443 serves HTTP/3. In IP mode, HTTP carries the owner and agent credentials in cleartext on the network, so use a trusted private network or tunnel.

## Start and use the stack

```sh
docker compose config --quiet
docker compose up -d --build
docker compose ps
curl -fsS https://subspace.example.com/healthz
```

Use the configured public URL in the health check and in the browser, then sign in with the owner token. Caddy routes `/mcp` and `/healthz` to the Go server, and routes owner REST and SSE through Next.js. Both the Next.js proxy and Caddy stream SSE with buffering disabled.

Build the owner CLI for your shell with `go build -o bin/subspacectl ./cmd/subspacectl`, then export `SUBSPACE_URL` with the public origin and `SUBSPACE_OWNER_TOKEN` with the owner token. Run `bin/subspacectl --help` for the supported commands. The CLI is also available at `/usr/local/bin/subspacectl` in the server image.

To view service logs, run `docker compose logs --tail=100 server web caddy backup`.

## Backups and restore

The backup service takes an online SQLite snapshot with `VACUUM INTO` when it first sees the database and daily thereafter. It writes `daily-YYYYMMDD.db` (UTC) under `$SUBSPACE_DATA/backups` and keeps the 14 newest daily snapshots by default. To take today's snapshot immediately, run `docker compose run --rm --no-deps backup --once`. An existing snapshot for the day is reused. The owner API's manual backup endpoint writes to the same directory.

To restore, choose and verify a snapshot with `sqlite3 SNAPSHOT.db 'PRAGMA integrity_check;'`. Stop the stack. Preserve the existing database and any matching `-wal` and `-shm` files in a separate directory, then copy the snapshot to the configured database path. Set ownership to `65532:65532` and mode `0600` on the restored file. Run `docker compose up -d` and check `/healthz` before resuming agent work.
