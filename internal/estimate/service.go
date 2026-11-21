package estimate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	taskLimit  = 100000
	agentLimit = 100000
	auditPage  = 1000
)

type Source interface {
	Completed(ctx context.Context) ([]*model.Task, error)
	Agents(ctx context.Context) ([]*model.Agent, error)
	Audit(ctx context.Context, taskID string) ([]*model.AuditEvent, error)
	Tokens(ctx context.Context) (map[string]float64, error)
	PlanTasks(ctx context.Context, id string) ([]*model.Task, error)
	Open(ctx context.Context, briefID string) ([]*model.Task, error)
}

type Service struct {
	src Source
	opt Options

	mu    sync.Mutex
	facts map[string]fact
}

type fact struct {
	updated time.Time
	minutes float64
}

func NewService(src Source, opt Options) *Service {
	return &Service{src: src, opt: opt, facts: map[string]fact{}}
}

type history struct {
	model  *Model
	agents map[string]*model.Agent
}

func (s *Service) history(ctx context.Context) (history, error) {
	tasks, err := s.src.Completed(ctx)
	if err != nil {
		return history{}, err
	}
	agents, err := s.src.Agents(ctx)
	if err != nil {
		return history{}, err
	}
	tokens, err := s.src.Tokens(ctx)
	if err != nil {
		return history{}, err
	}
	byID := make(map[string]*model.Agent, len(agents))
	for _, a := range agents {
		byID[a.ID] = a
	}
	samples := make([]Sample, 0, len(tasks))
	for _, t := range tasks {
		minutes, err := s.minutes(ctx, t)
		if err != nil {
			return history{}, err
		}
		var agent *model.Agent
		if t.Assignee != nil {
			agent = byID[*t.Assignee]
		}
		samples = append(samples, SampleOf(t, agent, minutes, tokens[t.ID]))
	}
	return history{model: Fit(samples, s.opt), agents: byID}, nil
}

func (s *Service) minutes(ctx context.Context, t *model.Task) (float64, error) {
	s.mu.Lock()
	f, ok := s.facts[t.ID]
	s.mu.Unlock()
	if ok && f.updated.Equal(t.UpdatedAt) {
		return f.minutes, nil
	}
	events, err := s.src.Audit(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	f = fact{updated: t.UpdatedAt, minutes: WorkMinutes(events)}
	s.mu.Lock()
	s.facts[t.ID] = f
	s.mu.Unlock()
	return f.minutes, nil
}

func (h history) estimates(tasks []*model.Task) map[string]Estimate {
	out := make(map[string]Estimate, len(tasks))
	for _, t := range tasks {
		var agent *model.Agent
		if t.Assignee != nil {
			agent = h.agents[*t.Assignee]
		}
		out[t.ID] = h.model.Estimate(KeyOf(t, agent))
	}
	return out
}

func (s *Service) report(ctx context.Context, id string, tasks []*model.Task) (Report, error) {
	h, err := s.history(ctx)
	if err != nil {
		return Report{}, err
	}
	return Summarize(id, tasks, h.estimates(tasks), h.model.Samples()), nil
}

func (s *Service) Brief(ctx context.Context, briefID string) (Report, error) {
	tasks, err := s.src.Open(ctx, briefID)
	if err != nil {
		return Report{}, err
	}
	return s.report(ctx, briefID, tasks)
}

func (s *Service) Plan(ctx context.Context, planID string) (Report, error) {
	tasks, err := s.src.PlanTasks(ctx, planID)
	if err != nil {
		return Report{}, err
	}
	return s.report(ctx, planID, tasks)
}

func (s *Service) ForTasks(ctx context.Context, tasks []*model.Task) (map[string]Estimate, error) {
	h, err := s.history(ctx)
	if err != nil {
		return nil, err
	}
	if !h.model.Sufficient() {
		return nil, nil
	}
	return h.estimates(tasks), nil
}

type StoreSource struct {
	Store store.Store
	Usage interface {
		Query(ctx context.Context, q usage.Query) ([]usage.Record, error)
	}
}

func (s StoreSource) Completed(ctx context.Context) ([]*model.Task, error) {
	return s.tasks(ctx, store.TaskFilter{Status: []string{model.StatusDone, model.StatusApproved}, Limit: taskLimit})
}

func (s StoreSource) Open(ctx context.Context, briefID string) ([]*model.Task, error) {
	var out []*model.Task
	err := s.Store.View(ctx, func(tx store.Tx) error {
		if _, err := tx.GetBrief(briefID); err != nil {
			return err
		}
		all, err := tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: taskLimit})
		for _, t := range all {
			if !Completed(t) {
				out = append(out, t)
			}
		}
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, model.NotFound("brief %s not found", briefID)
	}
	return out, err
}

func (s StoreSource) tasks(ctx context.Context, f store.TaskFilter) ([]*model.Task, error) {
	var out []*model.Task
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = tx.ListTasks(f)
		return err
	})
	return out, err
}

func (s StoreSource) Agents(ctx context.Context) ([]*model.Agent, error) {
	var out []*model.Agent
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = tx.ListAgents(store.AgentFilter{Limit: agentLimit})
		return err
	})
	return out, err
}

func (s StoreSource) Audit(ctx context.Context, taskID string) ([]*model.AuditEvent, error) {
	var out []*model.AuditEvent
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var since int64
		for {
			page, err := tx.ListAudit(store.AuditFilter{TaskID: taskID, SinceID: since, Limit: auditPage})
			if err != nil {
				return err
			}
			out = append(out, page...)
			if len(page) < auditPage {
				return nil
			}
			since = page[len(page)-1].ID
		}
	})
	return out, err
}

func (s StoreSource) Tokens(ctx context.Context) (map[string]float64, error) {
	out := map[string]float64{}
	if s.Usage == nil {
		return out, nil
	}
	recs, err := s.Usage.Query(ctx, usage.Query{})
	for _, r := range recs {
		out[r.TaskID] += float64(r.TokensIn + r.TokensOut)
	}
	return out, err
}

func (s StoreSource) PlanTasks(ctx context.Context, id string) ([]*model.Task, error) {
	var out []*model.Task
	err := s.Store.View(ctx, func(tx store.Tx) error {
		if _, err := tx.GetPlan(id); err != nil {
			return err
		}
		all, err := tx.ListTasks(store.TaskFilter{PlanID: id, Limit: taskLimit})
		for _, t := range all {
			if !Completed(t) {
				out = append(out, t)
			}
		}
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, model.NotFound("plan %s not found", id)
	}
	return out, err
}
