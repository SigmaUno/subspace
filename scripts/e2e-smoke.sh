#!/usr/bin/env bash
#
# Subspace end-to-end smoke test.
#
# Exercises only the documented REST and MCP endpoints against a running
# Subspace. Requires a running server plus:
#
#   SUBSPACE_URL          base URL, e.g. http://localhost:8080
#   SUBSPACE_OWNER_TOKEN  the owner bearer token
#
# The flow (see SPEC.md and docs/api.md):
#   health check -> owner registers leader/worker/reviewer (tokens held in
#   memory, never echoed) -> owner creates a brief -> leader joins over MCP
#   (initialize + Mcp-Session-Id, tools/list, get_instructions, whoami) and
#   proposes a plan with 2 tasks -> assert the worker's tools/list has no
#   leader/owner tools -> owner approves the plan -> leader assigns task 1 ->
#   worker claim/heartbeat/update_status/post_message/submit -> owner verifies
#   the board and task thread -> pause_all blocks claims then resume_all
#   restores them -> revoke the test agents.
#
# Prints PASS/FAIL per step and exits non-zero on any failure.
#
# Usage: SUBSPACE_URL=... SUBSPACE_OWNER_TOKEN=... ./scripts/e2e-smoke.sh

set -uo pipefail

SUBSPACE_URL="${SUBSPACE_URL:?SUBSPACE_URL is required (e.g. http://localhost:8080)}"
SUBSPACE_OWNER_TOKEN="${SUBSPACE_OWNER_TOKEN:?SUBSPACE_OWNER_TOKEN is required}"
SUBSPACE_URL="${SUBSPACE_URL%/}"
API="${SUBSPACE_URL}/api/v1"
MCP="${SUBSPACE_URL}/mcp"
ACCEPT="application/json, text/event-stream"

for bin in curl jq; do
  command -v "$bin" >/dev/null 2>&1 || { echo "ERROR: $bin is required" >&2; exit 2; }
done

# Unique-per-run agent ids so the test can be re-run against the same server.
RUN_ID="$$-$(date +%s)"
LEADER_ID="rex-$RUN_ID"
WORKER_ID="claude-$RUN_ID"
REVIEWER_ID="codex-$RUN_ID"

# Scratch space.
SS_TMP="$(mktemp -d)"
trap 'rm -rf "$SS_TMP"' EXIT

# State. Agent tokens are held in shell variables and never printed.
PASS_COUNT=0
FAIL_COUNT=0
FAILED_STEPS=()
NEXT_ID=0

LEADER_TOKEN=""; WORKER_TOKEN=""; REVIEWER_TOKEN=""
LEADER_SESSION=""; WORKER_SESSION=""
BRIEF_ID=""; PLAN_ID=""; TASK1_ID=""; TASK2_ID=""

RESP_CODE=""; RESP_BODY=""

# ---------------------------------------------------------------------------
# HTTP helper. Sets RESP_CODE (status) and RESP_BODY (body). Returns non-zero
# only if curl itself fails.
#   do_request <METHOD> <URL> <TOKEN|-> <DATA|-> [extra curl args, e.g. -H ...]
# ---------------------------------------------------------------------------
do_request() {
  local method="$1" url="$2" token="$3" data="$4"
  shift 4
  local -a args=(-sS -X "$method" -w '%{http_code}' \
    -o "$SS_TMP/body" -D "$SS_TMP/headers" -H "Accept: $ACCEPT")
  if [ "$token" != "-" ]; then
    args+=(-H "Authorization: Bearer $token")
  fi
  if [ "$data" != "-" ]; then
    args+=(-H "Content-Type: application/json" --data "$data")
  fi
  args+=("$@")
  args+=("$url")

  RESP_CODE="$(curl "${args[@]}" 2>"$SS_TMP/curl.err")" || {
    echo "    curl error: $(cat "$SS_TMP/curl.err")" >&2
    RESP_CODE="000"; RESP_BODY=""
    return 1
  }
  RESP_CODE="$(printf '%s' "$RESP_CODE" | tr -d '\r')"
  RESP_BODY="$(cat "$SS_TMP/body")"
  return 0
}

# ---------------------------------------------------------------------------
# MCP helpers (Streamable HTTP, JSON-RPC 2.0).
# ---------------------------------------------------------------------------
mcp_initialize() { # $1 token, $2 name of var to receive the Mcp-Session-Id
  local token="$1" outvar="$2"
  local body='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"subspace-e2e-smoke","version":"1.0.0"}}}'
  do_request POST "$MCP" "$token" "$body"
  local sid
  sid="$(awk 'BEGIN{IGNORECASE=1} /^mcp-session-id:/ {sub(/^[^:]*:[[:space:]]*/,""); gsub(/\r/,""); print; exit}' "$SS_TMP/headers")"
  eval "$outvar=\"\$sid\""
}

mcp_notify() { # $1 token, $2 session, $3 notification method
  local token="$1" session="$2" method="$3"
  do_request POST "$MCP" "$token" "{\"jsonrpc\":\"2.0\",\"method\":\"$method\"}" \
    -H "Mcp-Session-Id: $session"
}

mcp_tools_list() { # $1 token, $2 session
  local token="$1" session="$2"
  NEXT_ID=$((NEXT_ID+1))
  do_request POST "$MCP" "$token" "{\"jsonrpc\":\"2.0\",\"id\":$NEXT_ID,\"method\":\"tools/list\"}" \
    -H "Mcp-Session-Id: $session"
}

mcp_tool_call() { # $1 token, $2 session, $3 tool name, $4 JSON args object
  local token="$1" session="$2" name="$3" args="$4"
  NEXT_ID=$((NEXT_ID+1))
  local body
  body="$(jq -nc --arg name "$name" --argjson id "$NEXT_ID" --argjson args "$args" \
    '{jsonrpc:"2.0",id:$id,method:"tools/call",params:{name:$name,arguments:$args}}')"
  do_request POST "$MCP" "$token" "$body" -H "Mcp-Session-Id: $session"
}

tool_ok() { printf '%s' "$RESP_BODY" | jq -e '.result != null and .result.isError != true' >/dev/null 2>&1; }
check_body() { printf '%s' "$RESP_BODY" | jq -e "$1" >/dev/null 2>&1; }
sc() { printf '%s' "$RESP_BODY" | jq -r "$1"; }

# ---------------------------------------------------------------------------
# Steps. Each returns 0 (pass) or non-zero (fail).
# ---------------------------------------------------------------------------
step_health() {
  do_request GET "$SUBSPACE_URL/healthz" "-" "-" || return 1
  [ "$RESP_CODE" = "200" ] || { echo "    healthz: HTTP $RESP_CODE" >&2; return 1; }
  check_body '.status == "ok"' || { echo "    healthz: $RESP_BODY" >&2; return 1; }
}

register_agent() { # id role kind model provider outvar
  local id="$1" role="$2" kind="$3" model="$4" provider="$5" outvar="$6"
  local body
  body="$(jq -nc --arg id "$id" --arg role "$role" --arg kind "$kind" \
    --arg model "$model" --arg provider "$provider" \
    '{id:$id,role:$role,kind:$kind,model:$model,provider:$provider}')"
  do_request POST "$API/agents" "$SUBSPACE_OWNER_TOKEN" "$body" || return 1
  [ "$RESP_CODE" = "201" ] || { echo "    register $id: HTTP $RESP_CODE ${RESP_BODY}" >&2; return 1; }
  local tok
  tok="$(printf '%s' "$RESP_BODY" | jq -r '.connect.token')"
  [ -n "$tok" ] && [ "$tok" != "null" ] || { echo "    register $id: no token returned" >&2; return 1; }
  eval "$outvar=\"\$tok\""
  return 0
}

step_register() {
  register_agent "$LEADER_ID"   "leader"   "other"       "claude-sonnet-5-5" "anthropic" LEADER_TOKEN  || return 1
  register_agent "$WORKER_ID"   "worker"   "claude-code" "claude-sonnet-5-5" "anthropic" WORKER_TOKEN  || return 1
  register_agent "$REVIEWER_ID" "reviewer" "codex"       "gpt-5"            "openai"    REVIEWER_TOKEN || return 1
}

step_brief() {
  local body='{"title":"e2e smoke","body":"End-to-end smoke test brief.","repos":["SigmaUno/foo"],"max_cost_eur":50,"max_parallel_agents":10,"allowed_providers":["anthropic","openai","deepseek"]}'
  do_request POST "$API/briefs" "$SUBSPACE_OWNER_TOKEN" "$body" || return 1
  [ "$RESP_CODE" = "201" ] || { echo "    create brief: HTTP $RESP_CODE ${RESP_BODY}" >&2; return 1; }
  BRIEF_ID="$(printf '%s' "$RESP_BODY" | jq -r '.id')"
  [ -n "$BRIEF_ID" ] && [ "$BRIEF_ID" != "null" ] || { echo "    no brief id returned" >&2; return 1; }
}

step_leader_join() {
  [ -n "${LEADER_TOKEN:-}" ] || { echo "    leader token missing" >&2; return 1; }

  mcp_initialize "$LEADER_TOKEN" LEADER_SESSION
  [ "$RESP_CODE" = "200" ] || { echo "    initialize: HTTP $RESP_CODE ${RESP_BODY}" >&2; return 1; }
  [ -n "$LEADER_SESSION" ] || { echo "    initialize: no Mcp-Session-Id header" >&2; return 1; }

  mcp_notify "$LEADER_TOKEN" "$LEADER_SESSION" "notifications/initialized"
  [ "$RESP_CODE" = "202" ] || { echo "    notifications/initialized: HTTP $RESP_CODE" >&2; return 1; }

  mcp_tools_list "$LEADER_TOKEN" "$LEADER_SESSION"
  [ "$RESP_CODE" = "200" ] || { echo "    tools/list: HTTP $RESP_CODE" >&2; return 1; }
  check_body '[.result.tools[].name] | index("propose_plan") != null' \
    || { echo "    leader tools/list missing propose_plan" >&2; return 1; }

  mcp_tool_call "$LEADER_TOKEN" "$LEADER_SESSION" "get_instructions" "{}"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    get_instructions failed: $RESP_BODY" >&2; return 1; }
  check_body '.result.structuredContent.role == "leader"' \
    || { echo "    get_instructions role != leader" >&2; return 1; }

  mcp_tool_call "$LEADER_TOKEN" "$LEADER_SESSION" "whoami" "{}"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    whoami failed: $RESP_BODY" >&2; return 1; }
  printf '%s' "$RESP_BODY" | jq -e --arg id "$LEADER_ID" \
    '.result.structuredContent.agent.id == $id' >/dev/null 2>&1 \
    || { echo "    whoami agent id != $LEADER_ID" >&2; return 1; }
}

step_propose_plan() {
  [ -n "${BRIEF_ID:-}" ] && [ -n "${LEADER_SESSION:-}" ] || return 1
  local args
  args="$(jq -nc --arg bid "$BRIEF_ID" '{brief_id:$bid, tasks:[
    {ref:"t1",title:"Add login",spec:"Implement login",acceptance_criteria:["user can log in"],repo:"SigmaUno/foo",role:"worker",budget:{max_minutes:120,max_cost_eur:5}},
    {ref:"t2",title:"Add dashboard",spec:"Implement dashboard",acceptance_criteria:["dashboard shows stats"],repo:"SigmaUno/foo",role:"worker"}
  ]}')"
  mcp_tool_call "$LEADER_TOKEN" "$LEADER_SESSION" "propose_plan" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    propose_plan failed: $RESP_BODY" >&2; return 1; }
  PLAN_ID="$(sc '.result.structuredContent.plan.id')"
  [ -n "$PLAN_ID" ] && [ "$PLAN_ID" != "null" ] || { echo "    no plan id returned" >&2; return 1; }
  TASK1_ID="$(printf '%s' "$RESP_BODY" | jq -r '.result.structuredContent.plan.tasks[] | select(.title=="Add login") | .id')"
  TASK2_ID="$(printf '%s' "$RESP_BODY" | jq -r '.result.structuredContent.plan.tasks[] | select(.title=="Add dashboard") | .id')"
  [ -n "$TASK1_ID" ] && [ "$TASK1_ID" != "null" ] && [ -n "$TASK2_ID" ] && [ "$TASK2_ID" != "null" ] \
    || { echo "    could not resolve task ids from plan" >&2; return 1; }
}

step_worker_tools() {
  [ -n "${WORKER_TOKEN:-}" ] || return 1

  mcp_initialize "$WORKER_TOKEN" WORKER_SESSION
  [ "$RESP_CODE" = "200" ] && [ -n "$WORKER_SESSION" ] || { echo "    worker initialize failed" >&2; return 1; }
  mcp_notify "$WORKER_TOKEN" "$WORKER_SESSION" "notifications/initialized"
  [ "$RESP_CODE" = "202" ] || { echo "    worker notifications/initialized: HTTP $RESP_CODE" >&2; return 1; }
  mcp_tools_list "$WORKER_TOKEN" "$WORKER_SESSION"
  [ "$RESP_CODE" = "200" ] || { echo "    worker tools/list: HTTP $RESP_CODE" >&2; return 1; }

  local names
  names="$(printf '%s' "$RESP_BODY" | jq -c '[.result.tools[].name]')"

  local t
  for t in claim_task release_task submit get_instructions whoami heartbeat update_status post_message; do
    printf '%s' "$names" | jq -e --arg t "$t" 'index($t) != null' >/dev/null 2>&1 \
      || { echo "    worker tools/list missing '$t'" >&2; return 1; }
  done
  for t in propose_plan create_task update_task assign reassign board list_agents get_brief list_briefs capacity request_spawn list_reviews review; do
    printf '%s' "$names" | jq -e --arg t "$t" 'index($t) == null' >/dev/null 2>&1 \
      || { echo "    worker tools/list should not contain '$t'" >&2; return 1; }
  done
}

step_approve() {
  [ -n "${PLAN_ID:-}" ] || return 1
  do_request POST "$API/plans/$PLAN_ID/approve" "$SUBSPACE_OWNER_TOKEN" '{"note":"lgtm"}' || return 1
  [ "$RESP_CODE" = "200" ] || { echo "    approve plan: HTTP $RESP_CODE ${RESP_BODY}" >&2; return 1; }
  check_body '.status == "approved"' || { echo "    plan not approved: $RESP_BODY" >&2; return 1; }
}

step_assign() {
  [ -n "${TASK1_ID:-}" ] && [ -n "${LEADER_SESSION:-}" ] || return 1
  local args
  args="$(jq -nc --arg t "$TASK1_ID" --arg a "$WORKER_ID" '{task_id:$t, agent_id:$a}')"
  mcp_tool_call "$LEADER_TOKEN" "$LEADER_SESSION" "assign" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    assign failed: $RESP_BODY" >&2; return 1; }
  printf '%s' "$RESP_BODY" | jq -e --arg t "$TASK1_ID" --arg a "$WORKER_ID" \
    '.result.structuredContent.task.id == $t and .result.structuredContent.task.status == "assigned" and .result.structuredContent.task.assignee == $a' \
    >/dev/null 2>&1 || return 1
}

step_worker_work() {
  [ -n "${TASK1_ID:-}" ] && [ -n "${WORKER_SESSION:-}" ] || return 1
  local args

  args="$(jq -nc --arg t "$TASK1_ID" '{task_id:$t}')"
  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "claim_task" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    claim_task failed: $RESP_BODY" >&2; return 1; }
  check_body '.result.structuredContent.task.status == "in_progress"' || return 1

  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "heartbeat" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok && check_body '.result.structuredContent.ok == true' \
    || { echo "    heartbeat failed: $RESP_BODY" >&2; return 1; }

  args="$(jq -nc --arg t "$TASK1_ID" '{task_id:$t, status:"in_progress", progress_pct:50}')"
  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "update_status" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok && check_body '.result.structuredContent.task.progress_pct == 50' \
    || { echo "    update_status failed: $RESP_BODY" >&2; return 1; }

  args="$(jq -nc --arg t "$TASK1_ID" '{thread:("task:"+$t), body:"implemented login"}')"
  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "post_message" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    post_message failed: $RESP_BODY" >&2; return 1; }
  [ "$(sc '.result.structuredContent.message.body')" = "implemented login" ] || return 1

  args="$(jq -nc --arg t "$TASK1_ID" '{task_id:$t, pr_url:"https://example.com/SigmaUno/foo/pull/1", summary:"login done"}')"
  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "submit" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    submit failed: $RESP_BODY" >&2; return 1; }
  check_body '.result.structuredContent.task.status == "submitted"' || return 1
}

step_owner_verify() {
  [ -n "${BRIEF_ID:-}" ] && [ -n "${TASK1_ID:-}" ] || return 1

  do_request GET "$API/briefs/$BRIEF_ID/board" "$SUBSPACE_OWNER_TOKEN" "-" || return 1
  [ "$RESP_CODE" = "200" ] || { echo "    board: HTTP $RESP_CODE" >&2; return 1; }
  local status
  status="$(printf '%s' "$RESP_BODY" | jq -r --arg t "$TASK1_ID" \
    '.columns | to_entries[] | select(.value | map(.id) | index($t)) | .key')"
  case "$status" in
    submitted|in_review) : ;;
    *) echo "    task $TASK1_ID status '$status' (expected submitted/in_review)" >&2; return 1;;
  esac

  do_request GET "$API/threads/task/$TASK1_ID/messages" "$SUBSPACE_OWNER_TOKEN" "-" || return 1
  [ "$RESP_CODE" = "200" ] || { echo "    thread: HTTP $RESP_CODE" >&2; return 1; }
  check_body '.messages | map(.body) | index("implemented login") != null' \
    || { echo "    task thread missing the posted message" >&2; return 1; }
}

step_pause_resume() {
  [ -n "${TASK2_ID:-}" ] && [ -n "${LEADER_SESSION:-}" ] && [ -n "${WORKER_SESSION:-}" ] || return 1
  local args

  args="$(jq -nc --arg t "$TASK2_ID" --arg a "$WORKER_ID" '{task_id:$t, agent_id:$a}')"
  mcp_tool_call "$LEADER_TOKEN" "$LEADER_SESSION" "assign" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok || { echo "    assign task2 failed: $RESP_BODY" >&2; return 1; }

  do_request POST "$API/pause-all" "$SUBSPACE_OWNER_TOKEN" '{"reason":"e2e pause"}' || return 1
  [ "$RESP_CODE" = "200" ] && check_body '.paused == true' || { echo "    pause-all failed: $RESP_BODY" >&2; return 1; }

  args="$(jq -nc --arg t "$TASK2_ID" '{task_id:$t}')"
  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "claim_task" "$args"
  printf '%s' "$RESP_BODY" | jq -e '.result.isError == true and .result.structuredContent.error.code == "paused"' >/dev/null 2>&1 \
    || { echo "    claim under pause not blocked (expected error code 'paused'): $RESP_BODY" >&2; return 1; }

  do_request POST "$API/resume-all" "$SUBSPACE_OWNER_TOKEN" '{}' || return 1
  [ "$RESP_CODE" = "200" ] && check_body '.paused == false' || { echo "    resume-all failed: $RESP_BODY" >&2; return 1; }

  mcp_tool_call "$WORKER_TOKEN" "$WORKER_SESSION" "claim_task" "$args"
  [ "$RESP_CODE" = "200" ] && tool_ok && check_body '.result.structuredContent.task.status == "in_progress"' \
    || { echo "    claim after resume failed: $RESP_BODY" >&2; return 1; }
}

step_revoke() {
  local id rc=0
  for id in "$LEADER_ID" "$WORKER_ID" "$REVIEWER_ID"; do
    do_request POST "$API/agents/$id/revoke" "$SUBSPACE_OWNER_TOKEN" '{"reason":"e2e cleanup"}' || { rc=1; continue; }
    if [ "$RESP_CODE" = "200" ] && check_body '.status == "revoked"'; then
      :
    else
      echo "    revoke $id: HTTP $RESP_CODE ${RESP_BODY}" >&2
      rc=1
    fi
  done
  return $rc
}

run_step() { # $1 description, rest: function + args
  local desc="$1"; shift
  if "$@"; then
    PASS_COUNT=$((PASS_COUNT+1))
    printf 'PASS: %s\n' "$desc"
    return 0
  fi
  FAIL_COUNT=$((FAIL_COUNT+1))
  FAILED_STEPS+=("$desc")
  printf 'FAIL: %s\n' "$desc"
  return 1
}

main() {
  printf 'Subspace e2e smoke test\n'
  printf '  SUBSPACE_URL=%s\n' "$SUBSPACE_URL"
  printf '  (agent tokens are held in memory and never echoed)\n\n'

  run_step "health check"                                  step_health
  run_step "owner registers leader/worker/reviewer"        step_register
  run_step "owner creates brief"                           step_brief
  run_step "leader MCP join (initialize/tools/list/get_instructions/whoami)" step_leader_join
  run_step "leader proposes plan (2 tasks)"                step_propose_plan
  run_step "worker tools/list excludes leader/owner tools" step_worker_tools
  run_step "owner approves plan"                           step_approve
  run_step "leader assigns task 1 to worker"               step_assign
  run_step "worker claim/heartbeat/status/message/submit"  step_worker_work
  run_step "owner verifies board + task thread"            step_owner_verify
  run_step "pause_all blocks claims, resume_all restores"  step_pause_resume
  run_step "revoke test agents"                            step_revoke

  printf '\nResult: %d passed, %d failed\n' "$PASS_COUNT" "$FAIL_COUNT"
  if [ "$FAIL_COUNT" -ne 0 ]; then
    printf 'Failed steps:\n'
    local s
    for s in "${FAILED_STEPS[@]}"; do printf '  - %s\n' "$s"; done
    exit 1
  fi
  printf 'All steps passed.\n'
}

main "$@"
