# Capacity config example

`deploy/capacity.example.json` is an example for `SUBSPACE_CAPACITY_FILE` (JSON, see SPEC §8 and docs/api.md §3 `CapacityLimit`).
The owner can override it at runtime with `PUT /api/v1/capacity`, which is persisted in the DB and wins over the file.

## Fields

- `global_max_agents`: hard cap on non-revoked agents holding an in_progress task or a pending spawn. `300` matches the scale target (SPEC §8).
- `provider` / `model`: a limit applies to agents whose provider and model match. `"*"` matches anything.
- `max_concurrency`: agents of that provider/model that may be in use at once. `0` means unlimited.
- `requests_per_min`: informational only in Phase 0 (SPEC §14 Q2); not enforced.
- `key_env` (optional): name of a server environment variable holding an API key for this provider. Limits with a
  concrete provider and model and a `key_env` are probed with a 1-token request every
  `SUBSPACE_PROVIDER_PROBE_INTERVAL` (default `5m`); results are at `GET /api/v1/providers` and on the overview, and
  failures feed the provider circuit breaker. Names starting with `SUBSPACE_` are rejected.
- `base_url` (optional): API base URL; required for providers other than `anthropic`, `openai` and `deepseek`
  (OpenAI-compatible).

## Limits

| Provider | Model | max_concurrency | requests_per_min | Rationale |
|---|---|---:|---:|---|
| anthropic | `claude-opus-*` | 40 | 50 | Most expensive tier; keep the pool small to bound spend. |
| anthropic | `claude-sonnet-*` | 120 | 200 | Main coding workhorse; largest Anthropic share. |
| anthropic | `claude-haiku-*` | 80 | 400 | Cheap and fast; suits testers and light tasks. |
| deepseek | `deepseek-v4-pro` | 60 | 120 | Stronger DeepSeek tier, capped below flash. |
| deepseek | `deepseek-flash` | 100 | 300 | Cheapest high-throughput tier for the OpenCode workers. |
| openai | `codex` | 40 | 60 | Codex worker kind; modest share while it is being evaluated. |
| openai | `gpt-*` | 60 | 120 | General OpenAI models. |

The per-provider limits sum to more than 300 on purpose. `global_max_agents` is the real ceiling; the per-model limits
keep any single provider from taking the whole pool.

## Matching caveat

The current matcher (`internal/capacity`) only supports an exact model name or `"*"`. Entries with a trailing `*`
(`claude-opus-*`, `claude-sonnet-*`, `claude-haiku-*`, `gpt-*`) therefore do not match real model IDs yet, and
those limits are not enforced until glob or prefix matching is added. Enforcement today comes from the
exact-name entries, the `global_max_agents` cap, and the provider-level limits if you add `"*"` entries.
Replace the wildcards with exact model IDs if you need them enforced now.

## Usage

```sh
SUBSPACE_CAPACITY_FILE=/etc/subspace/capacity.json subspace
```
