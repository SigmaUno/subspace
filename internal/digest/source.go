package digest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	taskLimit     = 100000
	flappingPause = "flapping"
	minBucket     = time.Second
)

type DecisionFeed interface {
	List(ctx context.Context, limit int) ([]*decisions.Decision, error)
}

type Briefs interface {
	OpenBriefs(ctx context.Context) ([]string, error)
}

type Forecaster interface {
	Forecast(ctx context.Context, briefID string) (forecast.Forecast, error)
}

type StoreSource struct {
	Store    store.Store
	Feed     DecisionFeed
	Briefs   Briefs
	Forecast Forecaster
	Alerts   func() []spendguard.Status
}

func (s StoreSource) Snapshot(ctx context.Context, since, until time.Time) (Snapshot, error) {
	snap := Snapshot{Since: since, Until: until}
	bucket := max(until.Sub(since), minBucket)
	err := s.Store.View(ctx, func(tx store.Tx) error {
		ts, ok := tx.(store.TimeseriesTx)
		if !ok {
			return errors.New("store does not support timeseries")
		}
		sum := func(metric string) (float64, error) {
			pts, err := ts.Timeseries(store.TimeseriesQuery{Metric: metric, Since: since, Until: until, Bucket: bucket})
			var total float64
			for _, p := range pts {
				total += p.Value
			}
			return total, err
		}
		done, err := sum(store.MetricTasksDone)
		if err != nil {
			return err
		}
		created, err := sum(store.MetricTasksCreated)
		if err != nil {
			return err
		}
		tokens, err := sum(store.MetricTokens)
		if err != nil {
			return err
		}
		if snap.CostEUR, err = sum(store.MetricCostEUR); err != nil {
			return err
		}
		snap.Done, snap.Created, snap.Tokens = int(done), int(created), int64(tokens)
		blocked, err := tx.ListTasks(store.TaskFilter{Status: []string{model.StatusBlocked, model.StatusNeedsOwner}, Limit: taskLimit})
		if err != nil {
			return err
		}
		for _, t := range blocked {
			if !t.UpdatedAt.Before(since) {
				snap.Blocked++
			}
		}
		agents, err := tx.ListAgents(store.AgentFilter{Status: model.AgentPaused})
		if err != nil {
			return err
		}
		for _, a := range agents {
			if a.PauseReason != nil && strings.HasPrefix(*a.PauseReason, flappingPause) {
				snap.Flapping = append(snap.Flapping, a.ID)
			}
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.decisions(ctx, &snap); err != nil {
		return Snapshot{}, err
	}
	if err := s.risks(ctx, &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

func (s StoreSource) decisions(ctx context.Context, snap *Snapshot) error {
	all, err := s.Feed.List(ctx, 0)
	if err != nil {
		return err
	}
	for _, d := range all {
		if d.Kind == prsync.KindMergeReady {
			snap.MergeReady = append(snap.MergeReady, d.TaskID)
			continue
		}
		snap.Decisions = append(snap.Decisions, Pending{ID: d.ID, Title: d.Title, Priority: d.Priority})
	}
	return nil
}

func (s StoreSource) risks(ctx context.Context, snap *Snapshot) error {
	if s.Alerts != nil {
		for _, w := range s.Alerts() {
			var sev string
			switch w.Level {
			case spendguard.LevelExceeded:
				sev = SeverityHigh
			case spendguard.LevelWarning:
				sev = SeverityMedium
			default:
				continue
			}
			snap.Risks = append(snap.Risks, Risk{
				Source: SourceSpend, Severity: sev, Subject: string(w.Window),
				Message: fmt.Sprintf("%s spend at %.0f%% of its cap", w.Window, w.Percent),
			})
		}
	}
	if s.Briefs == nil || s.Forecast == nil {
		return nil
	}
	ids, err := s.Briefs.OpenBriefs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		f, err := s.Forecast.Forecast(ctx, id)
		if err != nil {
			return err
		}
		for _, r := range f.Risks {
			snap.Risks = append(snap.Risks, Risk{Source: SourceForecast, Severity: r.Severity, Subject: id, Message: r.Message})
		}
	}
	return nil
}
