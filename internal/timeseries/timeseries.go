package timeseries

import (
	"context"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const MaxBuckets = 2000

var bucketSizes = map[string]time.Duration{
	"5m": 5 * time.Minute,
	"1h": time.Hour,
	"1d": 24 * time.Hour,
}

var defaultSpan = map[string]time.Duration{
	"5m": 24 * time.Hour,
	"1h": 7 * 24 * time.Hour,
	"1d": 30 * 24 * time.Hour,
}

var metrics = map[string]bool{
	store.MetricTasksDone:    true,
	store.MetricTasksCreated: true,
	store.MetricCostEUR:      true,
	store.MetricActiveAgents: true,
	store.MetricTokens:       true,
	store.MetricReviewRounds: true,
}

type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

type Series struct {
	Metric  string    `json:"metric"`
	Bucket  string    `json:"bucket"`
	BriefID string    `json:"brief_id,omitempty"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Points  []Point   `json:"points"`
}

type Request struct {
	Metric  string
	Bucket  string
	Since   time.Time
	BriefID string
}

type Service struct {
	st  store.Store
	now func() time.Time
}

func New(st store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

func (s *Service) Series(ctx context.Context, r Request) (Series, error) {
	if !metrics[r.Metric] {
		return Series{}, model.BadRequest("metric must be one of tasks_done, tasks_created, cost_eur, active_agents, tokens, review_rounds")
	}
	if r.Bucket == "" {
		r.Bucket = "1h"
	}
	size, ok := bucketSizes[r.Bucket]
	if !ok {
		return Series{}, model.BadRequest("bucket must be one of 5m, 1h, 1d")
	}
	now := s.now().UTC()
	until := now.Truncate(size).Add(size)
	since := r.Since.UTC()
	if r.Since.IsZero() {
		since = now.Add(-defaultSpan[r.Bucket])
	}
	if !since.Before(until) {
		return Series{}, model.BadRequest("since must be in the past")
	}
	since = since.Truncate(size)
	if floor := until.Add(-MaxBuckets * size); since.Before(floor) {
		since = floor
	}
	var raw []store.TimeseriesPoint
	err := s.st.View(ctx, func(tx store.Tx) error {
		if r.BriefID != "" {
			if _, err := tx.GetBrief(r.BriefID); errors.Is(err, store.ErrNotFound) {
				return model.NotFound("brief %s not found", r.BriefID)
			} else if err != nil {
				return err
			}
		}
		ts, ok := tx.(store.TimeseriesTx)
		if !ok {
			return errors.New("store does not support timeseries")
		}
		var err error
		raw, err = ts.Timeseries(store.TimeseriesQuery{Metric: r.Metric, Since: since, Until: until, Bucket: size, BriefID: r.BriefID})
		return err
	})
	if err != nil {
		return Series{}, err
	}
	have := make(map[int64]float64, len(raw))
	for _, p := range raw {
		have[p.At.UnixMilli()] = p.Value
	}
	points := make([]Point, 0, int(until.Sub(since)/size))
	for t := since; t.Before(until); t = t.Add(size) {
		points = append(points, Point{T: t, V: have[t.UnixMilli()]})
	}
	return Series{Metric: r.Metric, Bucket: r.Bucket, BriefID: r.BriefID, Since: since, Until: until, Points: points}, nil
}
