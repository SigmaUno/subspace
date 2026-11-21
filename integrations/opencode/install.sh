#!/bin/sh
set -eu

url=${1:?usage: install.sh <subspace-url> [project-dir]}
dir=${2:-.}
here=$(cd "$(dirname "$0")" && pwd)

[ ! -e "$dir/opencode.json" ] || { echo "$dir/opencode.json exists; merge opencode.json.tmpl by hand" >&2; exit 1; }
mkdir -p "$dir/.opencode/plugins"
cp "$here/plugins/subspace.ts" "$dir/.opencode/plugins/subspace.ts"
sed -e "s|{{\.URL}}|${url%/}|g" "$here/opencode.json.tmpl" >"$dir/opencode.json"
echo "wrote $dir/opencode.json and $dir/.opencode/plugins/subspace.ts"
