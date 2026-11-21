#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

case "${1:-}" in
pg) run='TestAcceptanceSwarm$' ;;
ha) run='TestAcceptanceSwarmHA$' ;;
*) echo "usage: $0 pg|ha" >&2; exit 2 ;;
esac

case "${SUBSPACE_TEST_PG_DSN:-}" in
postgres://* | postgresql://*) ;;
*) echo "SUBSPACE_TEST_PG_DSN must be a postgres:// DSN of a throwaway database" >&2; exit 2 ;;
esac

unset SUBSPACE_TEST_DB
GOMAXPROCS=2 exec "${GO:-go}" test -race -count=1 -timeout 300s -v -run "$run" ./internal/acceptance/
