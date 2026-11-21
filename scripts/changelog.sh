#!/usr/bin/env bash
set -euo pipefail

to=${1:-HEAD}
from=${2:-}
if [ -z "$from" ]; then
  from=$(git describe --tags --abbrev=0 --match 'v*' "$to^" 2>/dev/null || true)
fi

declare -A section=(
  [breaking]="Breaking changes"
  [feat]="Features"
  [fix]="Fixes"
  [perf]="Performance"
  [refactor]="Refactoring"
  [docs]="Documentation"
  [test]="Tests"
  [maint]="Maintenance"
  [other]="Other"
)
declare -A lines
conventional='^([a-z]+)(\(([^)]*)\))?(!)?:[[:space:]]+(.+)$'

while IFS=$'\t' read -r hash subject; do
  group=other
  text=$subject
  if [[ $subject =~ $conventional ]]; then
    type=${BASH_REMATCH[1]}
    scope=${BASH_REMATCH[3]}
    text=${BASH_REMATCH[5]}
    [ -n "$scope" ] && text="**$scope:** $text"
    case $type in
      feat | fix | perf | refactor | docs | test) group=$type ;;
      build | ci | chore | style) group=maint ;;
    esac
    [ -n "${BASH_REMATCH[4]}" ] && group=breaking
  fi
  lines[$group]+="- $text ($hash)"$'\n'
done < <(git log --no-merges --format='%h%x09%s' "${from:+$from..}$to")

for group in breaking feat fix perf refactor docs test maint other; do
  [ -n "${lines[$group]:-}" ] || continue
  printf '## %s\n\n%s\n' "${section[$group]}" "${lines[$group]}"
done
