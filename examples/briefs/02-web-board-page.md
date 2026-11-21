---
title: "Web: board kanban page"
repos: ["SigmaUno/subspace"]
max_cost_eur: 60
max_parallel_agents: 4
allowed_providers: ["openai", "anthropic"]
---
Build the board page in the Next.js web app (`web/`). The page lists open briefs on the left and renders the selected brief's kanban board on the right, with one column per status (backlog through done). It reads `GET /api/v1/briefs/{id}/board` through the Next.js server-side proxy (owner token from the httpOnly cookie) and follows live updates over `GET /api/v1/events?topics=board` via SSE.

Acceptance criteria:
- The page renders one column per status for the selected brief.
- Live task moves appear without a full page reload.
- Empty and error states (no brief selected, 404, unauthenticated) are handled.

Notes for the leader:
- Reuse the existing proxy/route helpers; never call `/api/v1` directly from the browser.
- Column order follows the 12 statuses in docs/api.md §2.
- Handle the `lagged` SSE event by refetching the board.
