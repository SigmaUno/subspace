package mcp

import (
	"context"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/model"
)

func RegisterHillclimb(r *Registry, b *board.Board, m *hillclimb.Measurer) {
	r.Register(&Tool{
		Name: "measure",
		Description: "Run a hill-climb job's measurement command on a branch of your in-progress task in the job's repo and record the attempt. " +
			"Returns the metric, the loss (lower is better), the best so far and the job's remaining attempt budget.",
		Roles: WorkTester,
		Schema: obj(map[string]any{
			"job_id": str("hill-climb job id, e.g. H-1"),
			"branch": str("branch to measure: your task's branch or a variant of it (<task branch>-<suffix>)"),
		}, "job_id", "branch"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			JobID  string `json:"job_id"`
			Branch string `json:"branch"`
		}) (any, error) {
			if in.JobID == "" || in.Branch == "" {
				return nil, model.BadRequest("job_id and branch are required")
			}
			if p, why := b.Paused(); p {
				return nil, model.Errf("paused", "Subspace is paused: %s", why)
			}
			j, err := m.Store.GetJob(in.JobID)
			if err != nil {
				return nil, err
			}
			if err := measureAllowed(ctx, b, c.Actor, j, in.Branch); err != nil {
				return nil, err
			}
			a, j, err := m.Measure(ctx, j.ID, c.Actor.ID, in.Branch)
			if err != nil {
				return nil, err
			}
			var loss *float64
			if a.Value != nil {
				l := *a.Value
				if j.Direction == hillclimb.DirectionMax {
					l = -l
				}
				loss = &l
			}
			var remaining *int
			if j.Budget > 0 {
				n := max(j.Budget-j.Attempts, 0)
				remaining = &n
			}
			return map[string]any{
				"attempt": a, "metric": a.Value, "loss": loss, "best": j.Best,
				"job_status": j.Status, "stop_reason": j.StopReason, "remaining_attempts": remaining,
			}, nil
		}),
	})
}

func measureAllowed(ctx context.Context, b *board.Board, a model.Actor, j *hillclimb.Job, branch string) error {
	ts, err := b.ListMyTasks(ctx, a, []string{model.StatusInProgress})
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Assignee == nil || *t.Assignee != a.ID || t.BriefID != j.BriefID || t.Repo != j.Repo {
			continue
		}
		if branch != t.Branch && !strings.HasPrefix(branch, t.Branch+"-") {
			continue
		}
		if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR {
			return model.Errf("budget_exceeded", "task %s cost budget exhausted", t.ID)
		}
		if t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes {
			return model.Errf("budget_exceeded", "task %s time budget exhausted", t.ID)
		}
		br, err := b.GetBrief(ctx, t.BriefID)
		if err != nil {
			return err
		}
		if br.Budget.MaxCostEUR > 0 && br.CostUsed >= br.Budget.MaxCostEUR {
			return model.Errf("budget_exceeded", "brief %s cost budget exhausted", br.ID)
		}
		return nil
	}
	return model.Errf("not_assigned", "no in-progress task of yours covers job %s on branch %s", j.ID, branch)
}
