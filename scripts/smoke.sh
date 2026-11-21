#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_URL="http://127.0.0.1:8180"
WEB_URL="http://127.0.0.1:3100"
PROJECT="subspace-smoke-$$"
NETWORK="${PROJECT}-proxy"
HEALTH_TIMEOUT="${SMOKE_HEALTH_TIMEOUT:-90}"
ACCEPT="application/json, text/event-stream"
AGENT_ID="smoke-leader"

WORK=""
COMPOSE_TOUCHED=0
NETWORK_CREATED=0
CURRENT="setup"
PASSED=()
OWNER_TOKEN=""
AGENT_TOKEN=""
CODE=""

die() {
  printf '    %s\n' "$*" >&2
  exit 1
}

compose() {
  docker compose -p "$PROJECT" --env-file "$WORK/smoke.env" -f "$ROOT/docker-compose.yml" "$@"
}

redact() {
  local expr=()
  [ -z "$OWNER_TOKEN" ] || expr+=(-e "s|$OWNER_TOKEN|[redacted]|g")
  [ -z "$AGENT_TOKEN" ] || expr+=(-e "s|$AGENT_TOKEN|[redacted]|g")
  if [ "${#expr[@]}" -gt 0 ]; then sed "${expr[@]}"; else cat; fi
}

remove_work() {
  rm -rf "$WORK" 2>/dev/null && return 0
  docker run --rm -v "$WORK:/work" alpine:3.24 sh -c 'rm -rf /work/* /work/.[!.]*' >/dev/null 2>&1 || true
  rm -rf "$WORK"
}

teardown() {
  local rc=0
  if [ "$COMPOSE_TOUCHED" -eq 1 ]; then
    compose down --volumes --remove-orphans --timeout 5 >/dev/null 2>&1 || rc=1
  fi
  if [ "$NETWORK_CREATED" -eq 1 ]; then
    docker network rm "$NETWORK" >/dev/null 2>&1 || rc=1
  fi
  [ -z "$WORK" ] || remove_work || rc=1
  return "$rc"
}

finish() {
  local status=$?
  trap - EXIT
  set +e
  if [ "$status" -ne 0 ]; then
    printf 'FAIL: %s\n' "$CURRENT"
    if [ "$COMPOSE_TOUCHED" -eq 1 ]; then
      printf '\n--- compose logs (tokens redacted) ---\n'
      compose logs --no-color --tail=60 2>&1 | redact
    fi
  fi
  if ! teardown; then
    printf 'FAIL: teardown\n'
    [ "$status" -ne 0 ] || status=1
  fi
  printf '\n'
  local step
  for step in ${PASSED[@]+"${PASSED[@]}"}; do
    printf 'PASS: %s\n' "$step"
  done
  if [ "$status" -eq 0 ]; then
    printf '\nSmoke test passed: %d steps in %ds\n' "${#PASSED[@]}" "$SECONDS"
  else
    printf '\nSmoke test FAILED after %d passed steps in %ds\n' "${#PASSED[@]}" "$SECONDS"
  fi
  exit "$status"
}

step() {
  CURRENT="$1"
  shift
  "$@"
  PASSED+=("$CURRENT")
}

request() {
  CODE="$(curl -sS --max-time 20 -o "$WORK/body" -D "$WORK/headers" -w '%{http_code}' "$@")"
}

expect_code() {
  [ "$CODE" = "$1" ] || die "expected HTTP $1, got $CODE"
}

expect_json() {
  jq -e "$1" "$WORK/body" >/dev/null 2>&1 || die "$2"
}

header() {
  awk -v name="$1" 'tolower($1) == tolower(name) ":" { sub(/^[^:]*:[ \t]*/, ""); sub(/\r$/, ""); print; exit }' "$WORK/headers"
}

owner_web() {
  local method="$1" path="$2"
  shift 2
  request -X "$method" -b "$WORK/jar" -H "X-Subspace-Request: 1" -H "Accept: application/json" "$@" "$WEB_URL$path"
}

mcp() {
  local payload="$1"
  shift
  request -X POST -H "@$WORK/agent.hdr" -H "Accept: $ACCEPT" -H "Content-Type: application/json" \
    --data "$payload" "$@" "$SERVER_URL/mcp"
}

preflight() {
  local bin
  for bin in docker curl jq awk sed; do
    command -v "$bin" >/dev/null 2>&1 || die "$bin is required"
  done
  docker compose version >/dev/null 2>&1 || die "docker compose is required"
  if docker ps -a --format '{{.Names}}' | grep -Eqx 'subspace-(server|web|caddy|backup)'; then
    die "containers named subspace-* already exist; refusing to touch an existing deployment"
  fi
}

prepare() {
  WORK="$(mktemp -d)"
  local data="$WORK/data"
  mkdir -p "$data/db" "$data/backups" "$data/config" "$data/secrets"
  chmod 0777 "$data/db" "$data/backups"
  OWNER_TOKEN="$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')"
  printf '%s' "$OWNER_TOKEN" >"$data/secrets/owner_token"
  chmod 0444 "$data/secrets/owner_token"
  printf 'Authorization: Bearer %s\n' "$OWNER_TOKEN" >"$WORK/owner.hdr"
  printf '%s' "$OWNER_TOKEN" >"$WORK/owner.token"
  chmod 0600 "$WORK/owner.hdr" "$WORK/owner.token"
  cat >"$WORK/smoke.env" <<EOF
SUBSPACE_DATA=$data
SUBSPACE_PROXY_NETWORK=$NETWORK
SUBSPACE_PUBLIC_URL=$SERVER_URL
SUBSPACE_BACKUP_EVERY_SECONDS=3600
EOF
  docker network create "$NETWORK" >/dev/null
  NETWORK_CREATED=1
}

bring_up() {
  COMPOSE_TOUCHED=1
  compose build --quiet >"$WORK/compose.log" 2>&1 || { cat "$WORK/compose.log" >&2; die "compose build failed"; }
  compose up --detach >"$WORK/compose.log" 2>&1 || { cat "$WORK/compose.log" >&2; die "compose up failed"; }
}

wait_healthy() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT)) server=0 web=0
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ "$server" -eq 0 ] && curl -fsS --max-time 3 -o "$WORK/body" "$SERVER_URL/healthz" 2>/dev/null &&
      jq -e '.status == "ok"' "$WORK/body" >/dev/null 2>&1; then
      server=1
    fi
    if [ "$web" -eq 0 ] && curl -fsS --max-time 3 -o /dev/null "$WEB_URL/login" 2>/dev/null; then
      web=1
    fi
    [ "$server" -eq 1 ] && [ "$web" -eq 1 ] && return 0
    sleep 2
  done
  die "server healthy=$server web healthy=$web after ${HEALTH_TIMEOUT}s"
}

anonymous_rejected() {
  request "$SERVER_URL/api/v1/state"
  expect_code 401
  request "$WEB_URL/api/v1/state"
  expect_code 401
  request -X POST -H "Accept: $ACCEPT" -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}' \
    "$SERVER_URL/mcp"
  expect_code 401
  request -H "Authorization: Bearer invalid-$$" "$SERVER_URL/api/v1/state"
  expect_code 401
}

web_login() {
  local origin="$WEB_URL"
  request -X POST -H "Origin: $origin" --data-urlencode "token=rejected-$$-0000000000000000" "$WEB_URL/session"
  expect_code 303
  header location | grep -q 'error=' || die "bad token was not redirected to the login error page"
  request -X POST -c "$WORK/jar" -H "Origin: $origin" --data-urlencode "token@$WORK/owner.token" "$WEB_URL/session"
  expect_code 303
  [ "$(header location)" = "/" ] || die "login did not redirect to the workspace"
  grep -q 'subspace_owner' "$WORK/jar" || die "login did not set the session cookie"
  owner_web GET /api/v1/state
  expect_code 200
  expect_json '.counts.agents == 0' "fresh stack should have no agents"
}

register_agent() {
  local body
  body="$(jq -nc --arg id "$AGENT_ID" '{id:$id,role:"leader",kind:"other",model:"claude-sonnet-5-5",provider:"anthropic"}')"
  owner_web POST /api/v1/agents -H "Content-Type: application/json" --data "$body"
  expect_code 201
  expect_json '.agent.id == "'"$AGENT_ID"'" and (.connect.token | startswith("ssp_"))' "agent registration response is malformed"
  AGENT_TOKEN="$(jq -r '.connect.token' "$WORK/body")"
  printf 'Authorization: Bearer %s\n' "$AGENT_TOKEN" >"$WORK/agent.hdr"
  chmod 0600 "$WORK/agent.hdr"
  rm -f "$WORK/body"
}

mcp_session() {
  mcp '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"subspace-smoke","version":"1.0.0"}}}'
  expect_code 200
  expect_json '.result.protocolVersion != null and .result.serverInfo != null' "initialize result is malformed"
  local session
  session="$(header mcp-session-id)"
  [ -n "$session" ] || die "initialize returned no Mcp-Session-Id"
  printf 'Mcp-Session-Id: %s\n' "$session" >"$WORK/session.hdr"

  request -X POST -H "@$WORK/agent.hdr" -H "@$WORK/session.hdr" -H "Accept: $ACCEPT" \
    -H "Content-Type: application/json" --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$SERVER_URL/mcp"
  expect_code 202

  mcp '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' -H "@$WORK/session.hdr"
  expect_code 200
  expect_json '[.result.tools[].name] | index("get_instructions") != null and index("propose_plan") != null' \
    "tools/list is missing leader tools"

  mcp '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_instructions","arguments":{}}}' \
    -H "@$WORK/session.hdr"
  expect_code 200
  expect_json '.result.isError != true
    and .result.structuredContent.agent_id == "'"$AGENT_ID"'"
    and .result.structuredContent.role == "leader"
    and (.result.structuredContent.instructions | contains("Subspace"))' "get_instructions result is malformed"
}

brief_and_state() {
  local body id
  body="$(jq -nc '{title:"compose smoke",body:"Smoke test brief.",repos:[],max_cost_eur:1,max_parallel_agents:1,allowed_providers:[]}')"
  owner_web POST /api/v1/briefs -H "Content-Type: application/json" --data "$body"
  expect_code 201
  expect_json '.id | test("^B-[0-9]+$")' "brief creation returned no id"
  id="$(jq -r '.id' "$WORK/body")"

  owner_web GET "/api/v1/briefs/$id"
  expect_code 200
  expect_json '.title == "compose smoke" and .status == "open"' "created brief cannot be read back"

  owner_web GET "/api/v1/briefs/$id/board"
  expect_code 200
  expect_json '.brief.id == "'"$id"'" and (.columns | type == "object")' "brief board is malformed"

  owner_web GET /api/v1/state
  expect_code 200
  expect_json '.paused == false and .counts.agents == 1 and .counts.agents_active == 1' "state does not reflect the registered agent"
}

main() {
  cd "$ROOT"
  trap finish EXIT
  trap 'exit 130' INT TERM
  step "preflight checks" preflight
  step "generate owner token and environment" prepare
  step "build and start compose stack" bring_up
  step "server /healthz and web are up" wait_healthy
  step "anonymous access rejected on /api/v1 and /mcp" anonymous_rejected
  step "owner login through web with cookie jar" web_login
  step "register agent through the API" register_agent
  step "MCP initialize, tools/list and get_instructions" mcp_session
  step "create brief and read state" brief_and_state
  CURRENT="teardown"
}

main "$@"
