package mcp

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/model"
)

var helpServices sync.Map

var helpRoles = []string{model.RoleWorker, model.RoleTester, model.RoleReviewer}

func BindHelp(b *board.Board, s *helpreq.Service) { helpServices.Store(b, s) }

func RegisterHelp(r *Registry, b *board.Board) {
	svc := func() (*helpreq.Service, error) {
		s, ok := helpServices.Load(b)
		if !ok {
			return nil, model.Errf("internal", "help requests are not available")
		}
		return s.(*helpreq.Service), nil
	}
	r.Register(&Tool{
		Name: "request_help",
		Description: "Ask for help on a task without changing its status. Posts a structured message to the task and brief threads " +
			"and surfaces the request to the leader and owner.",
		Roles: helpRoles,
		Schema: obj(map[string]any{
			"task_id": str("task you are assigned to"),
			"kind":    enum("what is wrong", "unclear_spec", "missing_access", "conflicting_requirements", "too_big", "dependency_issue"),
			"details": str("specific description of the problem and what you need"),
		}, "task_id", "kind", "details"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			TaskID  string `json:"task_id"`
			Kind    string `json:"kind"`
			Details string `json:"details"`
		}) (any, error) {
			s, err := svc()
			if err != nil {
				return nil, err
			}
			req, err := s.RequestHelp(ctx, c.Actor, in.TaskID, in.Kind, in.Details)
			return map[string]any{"request": req}, err
		}),
	})
	r.Register(&Tool{
		Name: "report_blocker",
		Description: "Block your in_progress task (like update_status blocked) and record who must resolve it: the leader or the owner. " +
			"The right party is notified.",
		Roles: WorkTester,
		Schema: obj(map[string]any{
			"task_id": str("task you are working on"),
			"reason":  str("what blocks you"),
			"needs":   enum("who must unblock it", "leader", "owner"),
		}, "task_id", "reason", "needs"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			TaskID string `json:"task_id"`
			Reason string `json:"reason"`
			Needs  string `json:"needs"`
		}) (any, error) {
			s, err := svc()
			if err != nil {
				return nil, err
			}
			req, err := s.ReportBlocker(ctx, c.Actor, in.TaskID, in.Reason, in.Needs)
			return map[string]any{"request": req}, err
		}),
	})
	r.Register(&Tool{
		Name:        "suggest_split",
		Description: "Propose splitting a task into smaller tasks. The leader reviews the proposal and can accept it.",
		Roles:       helpRoles,
		Schema: obj(map[string]any{
			"task_id": str("task you are assigned to"),
			"proposed_tasks": map[string]any{
				"type": "array", "description": "2 to 20 replacement tasks; depends_on may name the ref of another proposed task",
				"items": obj(taskSpec, "title", "spec"),
			},
		}, "task_id", "proposed_tasks"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			TaskID        string             `json:"task_id"`
			ProposedTasks []helpreq.Proposed `json:"proposed_tasks"`
		}) (any, error) {
			s, err := svc()
			if err != nil {
				return nil, err
			}
			req, err := s.SuggestSplit(ctx, c.Actor, in.TaskID, in.ProposedTasks)
			return map[string]any{"request": req}, err
		}),
	})
	r.Register(&Tool{
		Name:        "list_help_requests",
		Description: "List help requests, blockers and split proposals raised by agents.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema: obj(map[string]any{
			"status":   enum("default open", "open", "accepted", "superseded", "closed", "all"),
			"brief_id": str("only this brief"),
			"task_id":  str("only this task"),
			"limit":    intg("maximum number of requests, 1-1000"),
		}),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			Status  string `json:"status"`
			BriefID string `json:"brief_id"`
			TaskID  string `json:"task_id"`
			Limit   int    `json:"limit"`
		}) (any, error) {
			s, err := svc()
			if err != nil {
				return nil, err
			}
			rs, err := s.ListFor(ctx, c.Actor, helpreq.Filter{Status: in.Status, BriefID: in.BriefID, TaskID: in.TaskID, Limit: in.Limit})
			return map[string]any{"requests": rs}, err
		}),
	})
	r.Register(&Tool{
		Name: "accept_split",
		Description: "Accept a split proposal: creates the proposed tasks with their dependencies, points tasks that depended on the original " +
			"at the new ones and cancels the original task.",
		Roles:  LeaderOnly,
		Schema: obj(map[string]any{"request_id": str("split proposal id, e.g. HR-3")}, "request_id"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			RequestID string `json:"request_id"`
		}) (any, error) {
			s, err := svc()
			if err != nil {
				return nil, err
			}
			res, err := s.AcceptSplit(ctx, c.Actor, in.RequestID)
			if err != nil {
				return nil, err
			}
			return res, nil
		}),
	})
}
