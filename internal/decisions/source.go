package decisions

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type StoreSource struct {
	Store   store.Store
	Overlay func(*model.Agent) *model.Agent
}

func (s StoreSource) Snapshot(ctx context.Context) (*Snapshot, error) {
	var out Snapshot
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		if out.Questions, err = tx.ListQuestions(store.QuestionFilter{Status: "open"}); err != nil {
			return err
		}
		if out.Plans, err = tx.ListPlans(store.PlanFilter{Status: model.PlanProposed}); err != nil {
			return err
		}
		for _, p := range out.Plans {
			if p.Tasks, err = tx.ListTasks(store.TaskFilter{PlanID: p.ID}); err != nil {
				return err
			}
		}
		statuses := []string{model.StatusApproved, model.StatusBlocked, model.StatusNeedsOwner}
		if out.Tasks, err = tx.ListTasks(store.TaskFilter{Status: statuses}); err != nil {
			return err
		}
		out.Agents, err = tx.ListAgents(store.AgentFilter{Status: model.AgentActive})
		return err
	})
	if err != nil {
		return nil, err
	}
	if s.Overlay != nil {
		for i, a := range out.Agents {
			out.Agents[i] = s.Overlay(a)
		}
	}
	out.Extra = contributed(ctx, s.Store)
	return &out, nil
}
