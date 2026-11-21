#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
GO="${GO:-go}"
SKIPPED=()

step() {
  printf '==> %s\n' "$*"
}

step "OpenAPI goldens"
"$GO" test ./internal/openapi -run Golden -update

step "web/lib/generated-types.ts"
"$GO" run ./cmd/gen-ts -o web/lib/generated-types.ts

step "tool lists in docs/api.md"
"$GO" run ./cmd/gen-api-docs | "$GO" run ./cmd/gen-sync

step "sdk/ts generated sources"
if ! command -v pnpm >/dev/null 2>&1; then
  SKIPPED+=("sdk/ts generated sources: pnpm not found; run: cd sdk/ts && pnpm install --frozen-lockfile && pnpm gen")
else
  if [ ! -d sdk/ts/node_modules ]; then
    (cd sdk/ts && pnpm install --frozen-lockfile)
  fi
  (cd sdk/ts && pnpm gen)
fi

step "gofmt"
gofmt -w .

if [ "${#SKIPPED[@]}" -gt 0 ]; then
  printf '\nnot regenerated:\n'
  printf '  - %s\n' "${SKIPPED[@]}"
fi

printf '\n'
"$GO" run ./cmd/gen-sync -manual
git status --short
