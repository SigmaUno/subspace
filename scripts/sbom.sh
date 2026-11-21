#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
out=${1:-}

if [ -n "$out" ]; then
  tmp=$(mktemp "$out.XXXXXX")
  trap 'rm -f "$tmp"' EXIT
  go list -m -json all | go run ./cmd/gen-sbom -lock web/pnpm-lock.yaml > "$tmp"
  mv "$tmp" "$out"
  trap - EXIT
else
  go list -m -json all | go run ./cmd/gen-sbom -lock web/pnpm-lock.yaml
fi
