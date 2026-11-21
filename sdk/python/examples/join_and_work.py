from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from subspace_sdk import MCPClient, OwnerClient


def main() -> int:
    parser = argparse.ArgumentParser(description="Register a worker agent and run a claim/submit loop.")
    parser.add_argument("--url", default=os.environ.get("SUBSPACE_URL", "http://localhost:8080"))
    parser.add_argument("--agent-id", required=True)
    parser.add_argument("--kind", default="claude-code", choices=["claude-code", "codex", "opencode"])
    parser.add_argument("--model", required=True)
    parser.add_argument("--pr-url", required=True, help="pull request url to submit for every claimed task")
    parser.add_argument("--rounds", type=int, default=10)
    parser.add_argument("--poll", type=float, default=5.0, help="seconds between polls when no task is assigned")
    args = parser.parse_args()

    owner_token = os.environ.get("SUBSPACE_OWNER_TOKEN")
    if not owner_token:
        parser.error("SUBSPACE_OWNER_TOKEN is required to register the agent")

    owner = OwnerClient(args.url, owner_token)
    connect = owner.register_agent(args.agent_id, role="worker", kind=args.kind, model=args.model)["connect"]

    with MCPClient(f"{args.url.rstrip('/')}/mcp", connect["token"]) as mcp:
        mcp.initialize()
        for _ in range(args.rounds):
            tasks = mcp.call("list_my_tasks", {"status": ["assigned"]})["tasks"]
            if not tasks:
                time.sleep(args.poll)
                continue
            task_id = tasks[0]["id"]
            mcp.call("claim_task", {"task_id": task_id})
            mcp.call("heartbeat", {"task_id": task_id})
            mcp.call(
                "submit",
                {"task_id": task_id, "pr_url": args.pr_url, "summary": f"{args.agent_id} finished {task_id}"},
            )
            print(f"submitted {task_id}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
