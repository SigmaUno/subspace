#!/bin/sh
set -eu

url=${1:?usage: install.sh <subspace-url> <subspace-hook-path> [dest]}
hook=${2:?usage: install.sh <subspace-url> <subspace-hook-path> [dest]}
dest=${3:-${CODEX_HOME:-$HOME/.codex}/subspace.config.toml}
here=$(cd "$(dirname "$0")" && pwd)

mkdir -p "$(dirname "$dest")"
sed -e "s|{{\.URL}}|${url%/}|g" -e "s|{{\.HookBin}}|$hook|g" "$here/config.toml.tmpl" >"$dest"
echo "wrote $dest; start Codex with: codex --profile subspace"
