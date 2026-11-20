package threads

import "github.com/SigmaUno/subspace/internal/model"

// CanAccess reports whether actor a may read or post to thread r (SPEC §5, docs/api.md §5).
//
// Owner may access every thread. Leader may access any agent thread (existence is the caller's
// concern), any task thread and any brief thread. Other agents may access their own agent thread
// and the task threads of tasks where they are assignee or reviewer; taskAssignee and taskReviewer
// are the task's fields ("" when unset) and are only consulted for task threads.
func CanAccess(a model.Actor, r Ref, taskAssignee, taskReviewer string) bool {
	if a.Role == model.RoleOwner {
		return true
	}
	switch r.Kind {
	case model.ThreadAgent:
		if a.Role == model.RoleLeader {
			return true
		}
		return a.ID != "" && r.ID == a.ID
	case model.ThreadTask:
		if a.Role == model.RoleLeader {
			return true
		}
		return a.ID != "" && (taskAssignee == a.ID || taskReviewer == a.ID)
	case model.ThreadBrief:
		return a.Role == model.RoleLeader
	}
	return false
}
