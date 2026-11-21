#!/bin/sh
set -eu

dest=${SUBSPACE_BACKUP_DIR:-/data/backups}
keep=${SUBSPACE_BACKUP_KEEP:-14}
interval=${SUBSPACE_BACKUP_EVERY_SECONDS:-86400}

positive_integer() {
    case "$1" in ''|*[!0-9]*) return 1 ;; esac
    [ "$1" -gt 0 ]
}

if ! positive_integer "$keep" || ! positive_integer "$interval"; then
    echo "Backup retention and interval must be positive integers." >&2
    exit 1
fi
umask 077
chmod 700 "$dest" 2>/dev/null || true

snapshot() (
    day=$(date -u +%Y%m%d) || exit 1
    target="$dest/pg-daily-$day.dump"

    if [ ! -f "$target" ]; then
        scratch=$(mktemp -d "$dest/.backup.XXXXXX") || exit 1
        trap 'rm -rf "$scratch"' EXIT
        trap 'exit 1' HUP INT TERM
        pg_dump --format=custom --no-owner --file="$scratch/snapshot.dump" || exit 1
        pg_restore --list "$scratch/snapshot.dump" >/dev/null || exit 1
        mv "$scratch/snapshot.dump" "$target" || exit 1
        echo "Created backup $target"
    fi

    set -- "$dest"/pg-daily-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].dump
    while [ "$#" -gt "$keep" ]; do
        rm -- "$1" || exit 1
        shift
    done
)

if [ "${1:-}" = "--once" ]; then
    snapshot
    exit
fi

while :; do
    if pg_isready -q && snapshot; then
        sleep "$interval" &
    else
        echo "Waiting to retry Postgres backup." >&2
        sleep 60 &
    fi
    wait "$!"
done
