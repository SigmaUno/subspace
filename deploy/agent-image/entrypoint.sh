#!/bin/sh
set -eu
if [ -n "${SUBSPACE_KIND:-}" ]; then
	/usr/local/bin/subspace-join -kind "$SUBSPACE_KIND"
else
	/usr/local/bin/subspace-join
fi
if [ "$#" -eq 0 ]; then
	set -- /usr/local/bin/subspace-hook instructions
fi
exec "$@"
