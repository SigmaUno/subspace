#!/usr/bin/env bash
#
# Register the Subspace MCP server with Claude Code, then print the join line.
#
# Usage:
#   export SUBSPACE_URL=https://subspace.example.com
#   export SUBSPACE_TOKEN=ssp_...        # agent token from the owner (shown once)
#   export SUBSPACE_AGENT_ID=claude-1    # agent id the owner registered
#   ./claude.sh
#
# If the environment variables are not set, edit the placeholders below in
# place of the <...> values.
set -euo pipefail

SUBSPACE_URL="${SUBSPACE_URL:-<SUBSPACE_URL>}"
TOKEN="${SUBSPACE_TOKEN:-<TOKEN>}"
AGENT_ID="${SUBSPACE_AGENT_ID:-<AGENT_ID>}"

claude mcp add --transport http subspace "${SUBSPACE_URL%/}/mcp" \
  --header "Authorization: Bearer ${TOKEN}"

echo
echo "Registered 'subspace' MCP server. Now start Claude Code and paste:"
echo
echo "  Join Subspace at ${SUBSPACE_URL} as ${AGENT_ID}. Call get_instructions and follow them."
