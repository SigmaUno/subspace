# Deploy Subspace with Docker Compose

## Production with the shared Caddy proxy

Run these steps from a Linux checkout that includes the Go server and the web app. The shared Caddy project in `/data/proxy` owns ports 80 and 443 and must already be attached to the Docker network `proxy`.

### Prepare persistent storage

The server and backup images run as UID and GID 65532.

```sh
export SUBSPACE_DATA=/data/subspace
sudo install -d -m 0755 "$SUBSPACE_DATA"
sudo install -d -m 0700 -o 65532 -g 65532 "$SUBSPACE_DATA/db" "$SUBSPACE_DATA/backups"
sudo install -d -m 0750 -o root -g 65532 "$SUBSPACE_DATA/config"
sudo install -d -m 0700 "$SUBSPACE_DATA/secrets"
cp .env.example .env
chmod 600 .env
```

Generate the owner token on the first install. Preserve the file across upgrades. The bind mount exposes it read-only to the Go server at `/run/secrets/subspace_owner_token`.

```sh
if ! sudo test -f "$SUBSPACE_DATA/secrets/owner_token"; then
  openssl rand -hex 32 | sudo install -m 0400 -o 65532 -g 65532 /dev/stdin "$SUBSPACE_DATA/secrets/owner_token"
fi
```

### Start and route traffic

The example environment defaults to `https://subspace.sigmauno.com` and the shared `proxy` network. Set `SUBSPACE_PROXY_NETWORK` if the shared network uses a different name.

```sh
docker network inspect "${SUBSPACE_PROXY_NETWORK:-proxy}" >/dev/null
docker compose config --quiet
docker compose up -d --build
docker compose ps
```

Give [the Subspace site block](caddy/subspace.caddy) to the shared proxy operator. Import it from the shared Caddyfile inside the proxy container using that project's existing import directory, then have the operator validate and reload the shared configuration. The site defaults to `subspace.sigmauno.com` with automatic TLS. If using a different name, set `SUBSPACE_DOMAIN` in the **shared Caddy container's** environment and set `SUBSPACE_PUBLIC_URL` in Subspace's `.env`. Point DNS at the proxy host. The site block uses the [Caddy request body directive](https://caddyserver.com/docs/caddyfile/directives/request_body) and requires Caddy 2.10 or newer.

```sh
curl -fsS https://subspace.sigmauno.com/healthz
```

Open `https://subspace.sigmauno.com` and sign in with the owner token. Caddy routes MCP and health checks to `subspace-server:8080` and the owner UI and REST API to `subspace-web:3000`. SSE streams without proxy buffering. Health checks and the sign-in page are reachable before authentication. The workspace and REST require the owner token, while MCP requires an agent token.

For SSH tunnels, forward the server's loopback ports. Open `http://localhost:3100` for the UI or use `http://localhost:8180` for the CLI.

```sh
ssh -N -L 3100:127.0.0.1:3100 -L 8180:127.0.0.1:8180 user@host
```

Build the CLI with `go build -o bin/subspacectl ./cmd/subspacectl`. Export `SUBSPACE_URL` with the public or tunneled origin and `SUBSPACE_OWNER_TOKEN` in your shell, then run `bin/subspacectl --help`. The CLI is also available at `/usr/local/bin/subspacectl` in the server image.

View service logs with `docker compose logs --tail=100 server web backup`.

## Standalone installations

Use this profile on hosts where the bundled Caddy can own ports 80 and 443. Prepare storage and the owner token as above. Create a dedicated Docker network, and set `SUBSPACE_PROXY_NETWORK=subspace-proxy` in `.env`.

Set `SUBSPACE_DOMAIN` to the host's DNS name for HTTPS. Leave it empty for HTTP on the host IP. Set `SUBSPACE_PUBLIC_URL` to the corresponding origin. For IP-only HTTP, use a trusted private network or an SSH tunnel because HTTP carries credentials in cleartext on the network.

```sh
docker network create subspace-proxy
sudo install -d -m 0700 "$SUBSPACE_DATA/caddy/data" "$SUBSPACE_DATA/caddy/config"
docker compose --profile standalone config --quiet
docker compose --profile standalone up -d --build
```

## Backups and restore

The backup service takes an online SQLite snapshot with `VACUUM INTO` when it first sees the database and daily thereafter. It writes `daily-YYYYMMDD.db` (UTC) under `$SUBSPACE_DATA/backups` and keeps the 14 newest daily snapshots by default. Run `docker compose run --rm --no-deps backup --once` to take today's snapshot immediately. An existing snapshot for the day is reused. The owner API's manual backup endpoint writes to the same directory.

To restore, choose and verify a snapshot with `sqlite3 SNAPSHOT.db 'PRAGMA integrity_check;'`. Stop the stack. Preserve the existing database and any matching `-wal` and `-shm` files in a separate directory, then copy the snapshot to the configured database path. Set ownership to `65532:65532` and mode `0600` on the restored file. Restart with the same Compose profile you used to start the stack and check `/healthz` before resuming agent work.
