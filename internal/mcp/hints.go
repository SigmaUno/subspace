package mcp

var codeHints = map[string]string{
	"bad_request":     "Fix the arguments named in the message and call again; tools/list shows each tool's input schema.",
	"unauthorized":    "Your token was rejected; stop and ask the owner for a new token.",
	"token_expired":   "Your token has expired; stop and ask the owner to rotate it.",
	"forbidden":       "This is outside your role or assignment; do not retry, and use ask_owner if you need it.",
	"not_assigned":    "This task is not assigned to you; call list_my_tasks and work only on those tasks.",
	"not_found":       "The id does not exist or is not visible to you; check it with list_my_tasks before retrying.",
	"conflict":        "The current state does not allow this; call get_task to see the status, then wait for the leader or act only on tasks assigned to you.",
	"capacity":        "A capacity limit is reached; wait and retry later, or use ask_owner if work is blocked.",
	"budget_exceeded": "The budget is used up; stop spending and call ask_owner with options such as raising the budget, narrowing the scope or cancelling.",
	"lease_expired":   "Your lease on this task expired; call claim_task again, then continue.",
	"quota_exceeded":  "A quota is used up; do not retry, and use ask_owner to request more or wait for the next UTC day.",
	"policy_denied":   "An owner policy forbids this action; do not retry, read the message, and use ask_owner if the work needs it.",
	"policy_hold":     "An owner policy holds this action for owner approval; do not retry now, work on something else and watch your thread for the decision.",
	"paused":          "Subspace is paused by the owner; call heartbeat until paused is false, then claim_task again.",
	"agent_paused":    "You are paused and write tools, including ask_owner, are refused; use read-only tools such as whoami and wait for the owner to resume you.",
	"rate_limited":    "You are calling too fast; wait a few seconds before retrying and avoid repeating identical calls.",
	"too_large":       "The input is too large; shorten it or split it across several calls.",
	"internal":        "The server failed; retry once after a short wait, and if it repeats use ask_owner and name the tool.",
}

var toolHints = map[string]map[string]string{
	"conflict": {
		"claim_task":    "The task cannot be claimed now: its status is not assigned or changes_requested, a dependency is not done, or its brief is closed; call get_task, wait for the leader, and claim only tasks assigned to you.",
		"submit":        "The task is not in_progress; call get_task, and if it is assigned claim_task again (the lease may have expired) before submitting.",
		"update_status": "The task is not in_progress; call get_task, and if it is assigned claim_task again (the lease may have expired) before updating.",
		"report_tests":  "The task is not in_progress; call get_task, and if it is assigned claim_task again (the lease may have expired) before reporting.",
		"release_task":  "The task is not in_progress; call get_task to see its status, there is nothing to release.",
		"review":        "The task is not in a reviewable status; call list_reviews for tasks waiting for you.",
		"assign":        "Only ready tasks can be assigned, and the agent must be active; call board or get_task, and use reassign for tasks that are already assigned.",
		"reassign":      "This task cannot be reassigned in its current status; call get_task and check the status first.",
		"create_task":   "The brief is closed or not accepting tasks; call get_brief, and use ask_owner if unsure.",
		"propose_plan":  "The brief is closed or already has a plan under decision; call get_brief, and wait for the owner.",
		"request_spawn": "The brief is closed or spawning is not allowed now; call get_brief and capacity, and use ask_owner if unsure.",
		"update_task":   "Only backlog or ready tasks can be edited; call get_task to see the status.",
	},
	"quota_exceeded": {
		"claim_task":   "Your daily agent quota is reached; finish or submit the task you hold, or use ask_owner to request a higher quota.",
		"assign":       "The target agent's daily quota is reached; pick another agent from list_agents.",
		"reassign":     "The target agent's daily quota is reached; pick another agent from list_agents.",
		"put_artifact": "The artifact storage quota is reached; remove or avoid large files, or use ask_owner to request more space.",
	},
	"capacity": {
		"request_spawn": "No capacity for another spawn; call capacity to see the limits, wait for running agents to finish, or use ask_owner.",
		"assign":        "No capacity to assign now; call capacity and list_agents for free slots, or wait.",
	},
	"not_assigned": {
		"review": "Another reviewer holds this task; call list_reviews and review only tasks listed there.",
	},
	"too_large": {
		"put_artifact": "The file exceeds the size limit; compress or split it.",
	},
}

func Hint(code, tool string) string {
	if h, ok := toolHints[code][tool]; ok {
		return h
	}
	return codeHints[code]
}
