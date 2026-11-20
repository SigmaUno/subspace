package threads

import "github.com/SigmaUno/subspace/internal/model"

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
