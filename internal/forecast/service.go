package forecast

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/usage"
)

const taskLimit = 100000

type Source interface {
	Snapshot(ctx context.Context, briefID string) (Snapshot, error)
	OpenBriefs(ctx context.Context) ([]string, error)
}

type UsageSource interface {
	Query(ctx context.Context, q usage.Query) ([]usage.Record, error)
}

type Service struct {
	src Source
	opt Options
	now func() time.Time
}

func NewService(src Source, opt Options, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{src: src, opt: opt, now: now}
}

func (s *Service) Forecast(ctx context.Context, briefID string) (Forecast, error) {
	snap, err := s.src.Snapshot(ctx, briefID)
	if err != nil {
		return Forecast{}, err
	}
	snap.Now = s.now()
	return Build(snap, s.opt), nil
}

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	ids, err := s.src.OpenBriefs(ctx)
	if err != nil {
		return nil, err
	}
	var out []*decisions.Decision
	for _, id := range ids {
		f, err := s.Forecast(ctx, id)
		if err != nil {
			return nil, err
		}
		if !f.CostOverrun() {
			continue
		}
		out = append(out, &decisions.Decision{
			ID:        "budget:" + id,
			Kind:      decisions.KindBudget,
			Title:     fmt.Sprintf("Forecast cost for %s exceeds its budget", id),
			Summary:   fmt.Sprintf("projected €%.2f (p50), €%.2f (p90) vs €%.2f budget; €%.2f used", f.Cost.Projected.P50, f.Cost.Projected.P90, f.Cost.Budget, f.Cost.Used),
			BriefID:   id,
			Links:     decisions.Links{Thread: model.ThreadBrief + ":" + id},
			CreatedAt: f.GeneratedAt,
		})
	}
	return out, nil
}

type Estimator interface {
	ForTasks(ctx context.Context, tasks []*model.Task) (map[string]estimate.Estimate, error)
}

type StoreSource struct {
	Store     store.Store
	Usage     UsageSource
	Estimator Estimator
}

func (s StoreSource) Snapshot(ctx context.Context, briefID string) (Snapshot, error) {
	var out Snapshot
	err := s.Store.View(ctx, func(tx store.Tx) error {
		b, err := tx.GetBrief(briefID)
		if err != nil {
			return err
		}
		out.Brief = *b
		if out.Brief.CostUsed, err = tx.BriefCost(briefID); err != nil {
			return err
		}
		out.Tasks, err = tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: taskLimit})
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, model.NotFound("brief %s not found", briefID)
	}
	if err != nil {
		return Snapshot{}, err
	}
	if s.Usage != nil {
		if out.Usage, err = s.Usage.Query(ctx, usage.Query{BriefID: briefID}); err != nil {
			return Snapshot{}, err
		}
	}
	if s.Estimator != nil {
		if out.Estimates, err = s.Estimator.ForTasks(ctx, openTasks(out.Tasks)); err != nil {
			return Snapshot{}, err
		}
	}
	return out, nil
}

func openTasks(tasks []*model.Task) []*model.Task {
	var out []*model.Task
	for _, t := range tasks {
		if t.Status != model.StatusCancelled && !estimate.Completed(t) {
			out = append(out, t)
		}
	}
	return out
}

func (s StoreSource) OpenBriefs(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.Store.View(ctx, func(tx store.Tx) error {
		bs, err := tx.ListBriefs(store.BriefFilter{Status: "open"})
		for _, b := range bs {
			ids = append(ids, b.ID)
		}
		return err
	})
	return ids, err
}
