#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

if git diff --cached --quiet --diff-filter=AM; then
  exit 0
fi

if ! SECRETSCAN_STAGED=1 go test ./deploy/ -run '^TestNoSecretsInStagedChanges$' -count=1 >/tmp/secretscan.$$ 2>&1; then
  grep 'likely secret' /tmp/secretscan.$$ >&2 || cat /tmp/secretscan.$$ >&2
  rm -f /tmp/secretscan.$$
  echo "pre-commit: likely secrets staged; remove them or add a documented entry to deploy/secretscan.allow" >&2
  exit 1
fi
rm -f /tmp/secretscan.$$
