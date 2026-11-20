#!/bin/sh
set -eu

db=${SUBSPACE_DB:-/data/db/subspace.db}
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

snapshot() (
    day=$(date -u +%Y%m%d) || exit 1
    target="$dest/daily-$day.db"

    if [ ! -f "$target" ]; then
        scratch=$(mktemp -d "$dest/.backup.XXXXXX") || exit 1
        trap 'rm -rf "$scratch"' EXIT
        trap 'exit 1' HUP INT TERM
        sql_path=$(printf '%s' "$scratch/snapshot.db" | sed "s/'/''/g") || exit 1
        sqlite3 -batch -bail -readonly -cmd '.timeout 15000' "$db" \
            "VACUUM INTO '$sql_path';" || exit 1
        mv "$scratch/snapshot.db" "$target" || exit 1
        echo "Created backup $target"
    fi

    set -- "$dest"/daily-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].db
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
    if [ -s "$db" ] && snapshot; then
        sleep "$interval" &
    else
        echo "Waiting to retry SQLite backup." >&2
        sleep 60 &
    fi
    wait "$!"
done
