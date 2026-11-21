---
title: "subspacectl: add --json output to list commands"
repos: ["SigmaUno/subspace"]
max_cost_eur: 30
max_parallel_agents: 3
allowed_providers: ["deepseek"]
---
Add a `--json` flag to the owner CLI (`subspacectl`) so its list commands (`briefs`, `tasks`, `agents`, `plans`, `questions`, `spawns`) can emit the raw JSON the API returns instead of the human-readable table.

Scope is deliberately small: one shared flag and formatter wired into each list command. No other command changes behavior.

Acceptance criteria:
- `subspacectl briefs --json` prints a JSON array of brief objects.
- `subspacectl tasks --json` prints a JSON array of task objects.
- Without `--json`, output is unchanged from today.
- Exit code 0 on success; non-zero with the API error on stderr otherwise.

Notes for the leader:
- Reuse the API response shape as-is; do not invent a new schema.
- Mention `--json` in each command's `--help` text.
