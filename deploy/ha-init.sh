#!/bin/sh
set -eu

data=${1:-${SUBSPACE_DATA:-/data/subspace}}

own() {
    uid=$1
    shift
    if [ "$(id -u)" -eq 0 ]; then
        chown -R "$uid:$uid" "$@"
    else
        docker run --rm --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add DAC_READ_SEARCH --security-opt no-new-privileges:true \
            -v "$data:$data" alpine:3.24 chown -R "$uid:$uid" "$@"
    fi
}

mkdir -p "$data/secrets" "$data/pg" "$data/shared" "$data/config" "$data/backups/pg" "$data/caddy/data" "$data/caddy/config"
chmod 700 "$data/secrets"
umask 077

if [ ! -s "$data/secrets/owner_token" ]; then
    od -An -vN32 -tx1 /dev/urandom | tr -d ' \n' >"$data/secrets/owner_token"
    chmod 444 "$data/secrets/owner_token"
fi

if [ ! -s "$data/secrets/pg_password" ]; then
    od -An -vN32 -tx1 /dev/urandom | tr -d ' \n' >"$data/secrets/pg_password"
    printf '*:*:*:subspace:%s\n' "$(cat "$data/secrets/pg_password")" >"$data/secrets/pgpass"
    own 70 "$data/secrets/pg_password"
    own 65532 "$data/secrets/pgpass"
fi

own 70 "$data/pg"
own 65532 "$data/shared" "$data/backups" "$data/caddy"
