#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${SUBSPACE_FORGEJO_IMAGE:-data.forgejo.org/forgejo/forgejo:11.0.6}"
LABEL="subspace.test=forgejo"
ADMIN="subspace-admin"
PACKAGES=(
  ./internal/githost/forgejo
  ./internal/webhook
  ./internal/prsync
  ./internal/forgestatus
  ./internal/reviewsync
  ./internal/mirror
  ./internal/mergequeue
)

die() {
  printf 'forgejo-live: %s\n' "$*" >&2
  exit 1
}

free_port() {
  local port
  for _ in $(seq 1 50); do
    port=$((20000 + RANDOM % 20000))
    if [ -z "$(ss -Htln "sport = :$port")" ]; then
      echo "$port"
      return 0
    fi
  done
  die "no free port found"
}

remove() {
  docker rm -f -v "$1" >/dev/null 2>&1 || true
}

up() {
  command -v docker >/dev/null || die "docker is required"
  if [ -n "$(docker ps -aq --filter "label=$LABEL")" ]; then
    die "a forgejo test container already exists; only one may run at a time"
  fi
  docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull -q "$IMAGE" >&2

  local name="subspace-forgejo-test-$$" port password token started=0
  port="$(free_port)"
  password="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')Aa1"

  trap '[ "$started" = 1 ] && remove "$name"' ERR
  docker run -d --name "$name" --label "$LABEL" \
    --memory 1g --cpus 1 --pids-limit 512 \
    --network host \
    -e FORGEJO__server__HTTP_ADDR=127.0.0.1 \
    -e "FORGEJO__server__HTTP_PORT=$port" \
    -e FORGEJO__security__INSTALL_LOCK=true \
    -e FORGEJO__database__DB_TYPE=sqlite3 \
    -e "FORGEJO__server__ROOT_URL=http://127.0.0.1:$port/" \
    -e "FORGEJO__server__DOMAIN=127.0.0.1" \
    -e FORGEJO__server__DISABLE_SSH=true \
    -e FORGEJO__server__OFFLINE_MODE=true \
    -e FORGEJO__service__DISABLE_REGISTRATION=true \
    -e FORGEJO__service__REQUIRE_SIGNIN_VIEW=true \
    -e FORGEJO__actions__ENABLED=false \
    -e FORGEJO__mailer__ENABLED=false \
    -e FORGEJO__log__LEVEL=Warn \
    -e "FORGEJO__api__MAX_RESPONSE_ITEMS=${SUBSPACE_FORGEJO_MAX_ITEMS:-25}" \
    -e "FORGEJO__webhook__ALLOWED_HOST_LIST=loopback" \
    -e FORGEJO__webhook__DELIVER_TIMEOUT=10 \
    "$IMAGE" >/dev/null
  started=1

  local ready=0
  for _ in $(seq 1 120); do
    if curl -fsS -m 2 "http://127.0.0.1:$port/api/healthz" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  [ "$ready" = 1 ] || { docker logs "$name" >&2 || true; die "forgejo did not become ready"; }

  docker exec -u git "$name" forgejo admin user create --admin \
    --username "$ADMIN" --email "$ADMIN@example.invalid" \
    --password "$password" --must-change-password=false >/dev/null
  token="$(docker exec -u git "$name" forgejo admin user generate-access-token \
    --username "$ADMIN" --token-name live-tests --scopes all --raw)"

  trap - ERR
  printf 'export SUBSPACE_FORGEJO_URL=%q\n' "http://127.0.0.1:$port"
  printf 'export SUBSPACE_FORGEJO_TOKEN=%q\n' "$token"
  printf 'export SUBSPACE_FORGEJO_ADMIN=%q\n' "$ADMIN"
    printf 'export SUBSPACE_FORGEJO_CONTAINER=%q\n' "$name"
}

CONTAINER=""

cleanup() {
  [ -z "$CONTAINER" ] || remove "$CONTAINER"
}

run() {
  local exports
  exports="$(up)"
  eval "$exports"
  CONTAINER="$SUBSPACE_FORGEJO_CONTAINER"
  trap cleanup EXIT
  trap 'exit 130' INT TERM
  cd "$ROOT"
  SUBSPACE_TEST_FORGEJO=1 GOMAXPROCS="${GOMAXPROCS:-4}" \
    go test -count=1 -p 1 -run 'Live' "$@" "${PACKAGES[@]}"
}

case "${1:-run}" in
  up) up ;;
  down) [ -n "${2:-}" ] || die "usage: down <container>"; remove "$2" ;;
  run) shift || true; run "$@" ;;
  *) die "usage: $0 [run [go test flags]|up|down <container>]" ;;
esac
