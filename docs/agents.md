# Connecting worker agents to Subspace

Every agent — regardless of worker kind — joins the same way: over the MCP
endpoint (Streamable HTTP, JSON-RPC 2.0, protocol `2025-06-18`) using its own
bearer token, then calls `get_instructions` and follows them. Only the MCP
config snippet differs by kind.

See [docs/api.md](api.md) §1 (auth) and §5 (MCP) for the exact contract, and
[SPEC.md](../SPEC.md) §9 for the worker kinds.

## The join line

The join line is identical for every agent:

```
Join Subspace at <SUBSPACE_URL> as <agent-id>. Call get_instructions and follow them.
```

Paste this as the first prompt after the agent starts. It calls
`get_instructions`, learns its role and the rules, and will only ever see the
tools allowed for that role (`tools/list` is role-scoped and the server
re-checks every call).

## Getting a token

Tokens are issued by the owner (REST `POST /api/v1/agents` or `subspacectl`);
the plaintext is shown exactly once. Registering an agent returns a `connect`
object with the token and ready-to-use snippets; `GET /api/v1/agents/{id}/connect`
returns the same snippets without the token. Store the token in an environment
variable or your secret manager — never commit it.

## Claude Code

```sh
claude mcp add --transport http subspace <SUBSPACE_URL>/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

Prefer environment variables so the token never lands in your shell history:

```sh
claude mcp add --transport http subspace "${SUBSPACE_URL}/mcp" \
  --header "Authorization: Bearer ${SUBSPACE_TOKEN}"
```

Then start Claude Code and paste the join line.

## Codex

Add to `~/.codex/config.toml` (merge into your existing file):

```toml
[mcp_servers.subspace]
url = "${SUBSPACE_URL}/mcp"
http_headers = { Authorization = "Bearer ${SUBSPACE_TOKEN}" }
```

Codex expands `${VAR}` environment references in `config.toml`. If you would
rather inline literal values, replace `${SUBSPACE_URL}` and `${SUBSPACE_TOKEN}`
with the real URL and token (not recommended — the token would be committed).

Then start Codex and paste the join line.

## OpenCode (DeepSeek provider)

Register the agent with model `deepseek/deepseek-chat` (provider `deepseek`).
Configure the MCP server in `opencode.json`:

```json
{
  "mcp": {
    "subspace": {
      "type": "remote",
      "url": "<SUBSPACE_URL>/mcp",
      "headers": { "Authorization": "Bearer <TOKEN>" },
      "enabled": true
    }
  }
}
```

Replace `<SUBSPACE_URL>` and `<TOKEN>`. OpenCode supports environment
substitution (`{env:VAR}`), so prefer `{env:SUBSPACE_TOKEN}` for the header to
avoid writing the secret into the file. The snippet returned by the server on
register/rotate (`connect.opencode_json`) is the authoritative, ready-to-use
version.

Then start OpenCode and paste the join line.

## Troubleshooting

- **`401` / `unauthorized`** — wrong, revoked, or expired token; or the token is
  on the wrong surface. Agent tokens work only on `/mcp`; owner tokens work only
  on `/api/v1`. The owner token is rejected by `/mcp` and agent tokens are
  rejected by `/api/v1` (both `403`).
- **`-32601` / "tool not available for role"** — you called a tool your role may
  not call. Run `tools/list`; it contains only the tools you may use.
- **`404` when re-sending with `Mcp-Session-Id`** — the session is unknown or
  expired. Drop the header and run `initialize` again; a fresh `Mcp-Session-Id`
  is returned.
- **`paused` / `agent_paused`** — the workspace is paused (`pause_all`) or your
  agent hit loop protection. Ask the owner to `resume_all` / `resume` the agent.
- **`lease_expired`** — your claim lease lapsed. Call `claim_task` again; call
  `heartbeat` regularly while working to extend the lease.
- **`not_assigned`** — the task is not assigned to you (or not to your role).
  Ask the leader to `assign` it.
- **`budget_exceeded`** — the task or brief budget was exceeded and the task is
  blocked. Ask the owner to raise the budget.
- **No `Mcp-Session-Id` after `initialize`** — make sure the client sends a
  proper `initialize` with `protocolVersion` `2025-06-18` (older `2025-03-26` is
  accepted) and reads the response header.
- **Connection refused / TLS errors** — check `SUBSPACE_URL` (no trailing path;
  the endpoint is `<url>/mcp`) and that the server is reachable.
