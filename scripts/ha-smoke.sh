#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="subspace-ha-smoke-$$"
HTTP_PORT="${HA_SMOKE_HTTP_PORT:-18480}"
HTTPS_PORT="${HA_SMOKE_HTTPS_PORT:-18443}"
BASE="http://127.0.0.1:$HTTP_PORT"
HEALTH_TIMEOUT="${HA_SMOKE_HEALTH_TIMEOUT:-180}"
FAILOVER_TIMEOUT="${HA_SMOKE_FAILOVER_TIMEOUT:-60}"
ACCEPT="application/json, text/event-stream"
REPLICAS=(ha-server-a ha-server-b)

WORK=""
DATA=""
COMPOSE_TOUCHED=0
CURRENT="setup"
PASSED=()
OWNER_TOKEN=""
LEADER_TOKEN=""
WORKER_TOKEN=""
CODE=""
LEADER_SVC=""
SURVIVOR_SVC=""
TASK_ID=""
LEADER_SESSION=""

die() {
  printf '    %s\n' "$*" >&2
  exit 1
}

compose() {
  docker compose -p "$PROJECT" --env-file "$WORK/ha.env" -f "$ROOT/docker-compose.yml" --profile ha "$@"
}

redact() {
  local expr=() t
  for t in "$OWNER_TOKEN" "$LEADER_TOKEN" "$WORKER_TOKEN"; do
    [ -z "$t" ] || expr+=(-e "s|$t|[redacted]|g")
  done
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
      compose logs --no-color --tail=40 2>&1 | redact
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
    printf '\nHA smoke test passed: %d steps in %ds\n' "${#PASSED[@]}" "$SECONDS"
  else
    printf '\nHA smoke test FAILED after %d passed steps in %ds\n' "${#PASSED[@]}" "$SECONDS"
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
  CODE="$(curl -sS --max-time 20 -o "$WORK/body" -D "$WORK/headers" -w '%{http_code}' "$@" 2>/dev/null)" || CODE=000
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

until_ok() {
  local timeout="$1" what="$2"
  shift 2
  local deadline=$((SECONDS + timeout))
  while ! "$@"; do
    [ "$SECONDS" -lt "$deadline" ] || die "$what did not happen within ${timeout}s"
    sleep 1
  done
}

owner() {
  local method="$1" path="$2"
  shift 2
  request -X "$method" -H "@$WORK/owner.hdr" -H "Accept: application/json" "$@" "$BASE$path"
}

mcp() {
  local hdr="$1" payload="$2"
  shift 2
  request -X POST -H "@$hdr" -H "Accept: $ACCEPT" -H "Content-Type: application/json" --data "$payload" "$@" "$BASE/mcp"
}

mcp_call() {
  local hdr="$1" session="$2" name="$3" args="$4"
  mcp "$hdr" "$(jq -nc --arg n "$name" --argjson a "$args" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" \
    -H "@$session"
}

replica_ha() {
  compose exec -T ha-caddy sh -c 'read -r t; wget -q -O - --header "Authorization: Bearer $t" "http://$0:8080/api/v1/ha"' "$1" \
    <"$WORK/owner.token" >"$WORK/ha-$1.json" 2>/dev/null
}

other_replica() {
  [ "$1" = "ha-server-a" ] && echo ha-server-b || echo ha-server-a
}

preflight() {
  local bin
  for bin in docker curl jq awk sed od tr; do
    command -v "$bin" >/dev/null 2>&1 || die "$bin is required"
  done
  docker compose version >/dev/null 2>&1 || die "docker compose is required"
}

prepare() {
  WORK="$(mktemp -d)"
  DATA="$WORK/data"
  sh "$ROOT/deploy/ha-init.sh" "$DATA" >/dev/null
  OWNER_TOKEN="$(cat "$DATA/secrets/owner_token")"
  printf 'Authorization: Bearer %s\n' "$OWNER_TOKEN" >"$WORK/owner.hdr"
  printf '%s' "$OWNER_TOKEN" >"$WORK/owner.token"
  chmod 0600 "$WORK/owner.hdr" "$WORK/owner.token"
  cat >"$WORK/ha.env" <<EOF
SUBSPACE_DATA=$DATA
SUBSPACE_HA_BIND=127.0.0.1
SUBSPACE_HA_HTTP_PORT=$HTTP_PORT
SUBSPACE_HA_HTTPS_PORT=$HTTPS_PORT
SUBSPACE_PUBLIC_URL=$BASE
SUBSPACE_LEASE=10s
SUBSPACE_BACKUP_EVERY_SECONDS=3600
EOF
}

bring_up() {
  COMPOSE_TOUCHED=1
  compose up --detach --build ha-caddy ha-backup >"$WORK/compose.log" 2>&1 || { redact <"$WORK/compose.log" >&2; die "compose up failed"; }
}

edge_ready() {
  request "$BASE/healthz"
  [ "$CODE" = 200 ] && jq -e '.status == "ok"' "$WORK/body" >/dev/null 2>&1
}

replicas_answer() {
  local r
  for r in "${REPLICAS[@]}"; do
    replica_ha "$r" || return 1
    jq -e '.backend == "postgres"' "$WORK/ha-$r.json" >/dev/null 2>&1 || return 1
  done
}

wait_healthy() {
  until_ok "$HEALTH_TIMEOUT" "edge /healthz" edge_ready
  until_ok "$HEALTH_TIMEOUT" "both replicas answering /api/v1/ha" replicas_answer
}

one_leader() {
  replicas_answer || return 1
  [ "$(jq -s '[.[] | select(.leader)] | length' "$WORK/ha-ha-server-a.json" "$WORK/ha-ha-server-b.json")" = 1 ]
}

find_leader() {
  until_ok "$HEALTH_TIMEOUT" "a single leader" one_leader
  local r
  for r in "${REPLICAS[@]}"; do
    if jq -e '.leader' "$WORK/ha-$r.json" >/dev/null; then LEADER_SVC="$r"; fi
  done
  SURVIVOR_SVC="$(other_replica "$LEADER_SVC")"
  jq -e --arg svc "$LEADER_SVC" '.leader == false and (.leader_instance | startswith($svc))' "$WORK/ha-$SURVIVOR_SVC.json" >/dev/null ||
    die "follower does not report $LEADER_SVC as leader"
}

owner_through_edge() {
  owner GET /api/v1/me
  expect_code 200
  expect_json '.scope == "owner"' "owner token not accepted through the edge"
  request "$BASE/api/v1/state"
  expect_code 401
}

register_agent() {
  local id="$1" role="$2" kind="$3" model="$4" provider="$5" var="$6" body token
  body="$(jq -nc --arg id "$id" --arg role "$role" --arg kind "$kind" --arg model "$model" --arg provider "$provider" \
    '{id:$id,role:$role,kind:$kind,model:$model,provider:$provider}')"
  owner POST /api/v1/agents -H "Content-Type: application/json" --data "$body"
  expect_code 201
  token="$(jq -r '.connect.token' "$WORK/body")"
  printf 'Authorization: Bearer %s\n' "$token" >"$WORK/$id.hdr"
  chmod 0600 "$WORK/$id.hdr"
  printf -v "$var" '%s' "$token"
  rm -f "$WORK/body"
}

open_session() {
  local hdr="$1" out="$2"
  mcp "$hdr" '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"ha-smoke","version":"1"}}}'
  expect_code 200
  local session
  session="$(header mcp-session-id)"
  [ -n "$session" ] || die "initialize returned no Mcp-Session-Id"
  printf 'Mcp-Session-Id: %s\n' "$session" >"$out"
  chmod 0600 "$out"
  request -X POST -H "@$hdr" -H "@$out" -H "Accept: $ACCEPT" -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BASE/mcp"
  expect_code 202
}

claim_a_task() {
  local body brief plan
  register_agent ha-leader leader other claude-sonnet-5-5 anthropic LEADER_TOKEN
  register_agent ha-worker worker claude-code claude-sonnet-5-5 anthropic WORKER_TOKEN

  body='{"title":"ha smoke","body":"HA smoke test brief.","repos":["SigmaUno/foo"],"max_cost_eur":10,"max_parallel_agents":2,"allowed_providers":[]}'
  owner POST /api/v1/briefs -H "Content-Type: application/json" --data "$body"
  expect_code 201
  brief="$(jq -r '.id' "$WORK/body")"
  printf '%s' "$brief" >"$WORK/brief.id"

  open_session "$WORK/ha-leader.hdr" "$WORK/leader.session"
  open_session "$WORK/ha-worker.hdr" "$WORK/worker.session"
  LEADER_SESSION="$WORK/leader.session"

  body="$(jq -nc --arg b "$brief" '{brief_id:$b,tasks:[{ref:"t1",title:"Lease probe",spec:"Claimed and abandoned.",acceptance_criteria:["none"],repo:"SigmaUno/foo",role:"worker",budget:{max_minutes:60,max_cost_eur:1}}]}')"
  mcp_call "$WORK/ha-leader.hdr" "$LEADER_SESSION" propose_plan "$body"
  expect_code 200
  expect_json '.result.isError != true' "propose_plan failed"
  plan="$(jq -r '.result.structuredContent.plan.id' "$WORK/body")"
  TASK_ID="$(jq -r '.result.structuredContent.plan.tasks[0].id' "$WORK/body")"

  owner POST "/api/v1/plans/$plan/approve" -H "Content-Type: application/json" --data '{"note":"ha smoke"}'
  expect_code 200

  mcp_call "$WORK/ha-leader.hdr" "$LEADER_SESSION" assign "$(jq -nc --arg t "$TASK_ID" '{task_id:$t,agent_id:"ha-worker"}')"
  expect_code 200
  expect_json '.result.isError != true' "assign failed"

  mcp_call "$WORK/ha-worker.hdr" "$WORK/worker.session" claim_task "$(jq -nc --arg t "$TASK_ID" '{task_id:$t}')"
  expect_code 200
  expect_json '.result.structuredContent.task.status == "in_progress"' "claim_task did not start the task"
}

kill_leader() {
  compose kill "$LEADER_SVC" >/dev/null 2>&1 || die "could not kill $LEADER_SVC"
}

survivor_leads() {
  replica_ha "$SURVIVOR_SVC" && jq -e '.leader == true' "$WORK/ha-$SURVIVOR_SVC.json" >/dev/null 2>&1
}

survivor_takes_over() {
  until_ok "$FAILOVER_TIMEOUT" "$SURVIVOR_SVC becoming leader" survivor_leads
}

task_returned() {
  owner GET "/api/v1/tasks/$TASK_ID"
  [ "$CODE" = 200 ] && jq -e '.status == "assigned"' "$WORK/body" >/dev/null 2>&1
}

sweeper_continues() {
  until_ok "$FAILOVER_TIMEOUT" "the expired lease of $TASK_ID returning to assigned" task_returned
}

board_works() {
  local brief
  brief="$(cat "$WORK/brief.id")"
  owner GET "/api/v1/briefs/$brief/board"
  [ "$CODE" = 200 ] && jq -e --arg t "$TASK_ID" '[.columns[][] | select(.id == $t)] | length == 1' "$WORK/body" >/dev/null 2>&1
}

board_after_failover() {
  until_ok "$FAILOVER_TIMEOUT" "the board answering through the edge" board_works
  owner POST /api/v1/briefs -H "Content-Type: application/json" \
    --data '{"title":"after failover","body":"Written with one replica down.","repos":[],"max_cost_eur":1,"max_parallel_agents":1,"allowed_providers":[]}'
  expect_code 201
  owner GET /api/v1/state
  expect_code 200
  expect_json '.paused == false and .counts.agents == 2' "state does not reflect the registered agents"
}

mcp_session_survives() {
  mcp_call "$WORK/ha-leader.hdr" "$LEADER_SESSION" whoami '{}'
  expect_code 200
  expect_json '.result.isError != true and .result.structuredContent.agent.id == "ha-leader"' "MCP session from before the failover is not accepted"
}

follower_ready() {
  replica_ha "$LEADER_SVC" && jq -e '.leader == false and .leader_instance != null' "$WORK/ha-$LEADER_SVC.json" >/dev/null 2>&1
}

old_leader_rejoins() {
  compose start "$LEADER_SVC" >/dev/null 2>&1 || die "could not restart $LEADER_SVC"
  until_ok "$HEALTH_TIMEOUT" "$LEADER_SVC rejoining as follower" follower_ready
}

backup_written() {
  compose exec -T ha-backup sh -c 'ls /data/backups' 2>/dev/null | grep -Eq '^pg-daily-[0-9]{8}\.dump$'
}

backup_present() {
  until_ok 120 "a pg_dump backup" backup_written
}

main() {
  cd "$ROOT"
  trap finish EXIT
  trap 'exit 130' INT TERM
  step "preflight checks" preflight
  step "initialise data directory and secrets" prepare
  step "build and start the ha profile" bring_up
  step "edge and both replicas are up" wait_healthy
  step "exactly one leader elected" find_leader
  step "owner API reachable through the edge" owner_through_edge
  step "agents register and a task is claimed with a short lease" claim_a_task
  step "kill the leader replica" kill_leader
  step "surviving replica takes over leadership" survivor_takes_over
  step "lease sweeper continues on the surviving replica" sweeper_continues
  step "board and writes keep working through the edge" board_after_failover
  step "MCP sessions opened before the failover stay valid" mcp_session_survives
  step "killed replica rejoins as follower" old_leader_rejoins
  step "postgres backup was written" backup_present
  CURRENT="teardown"
}

main "$@"
