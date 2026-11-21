package server

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/search"
)

func init() {
	registerMount(func(d Deps) {
		now := d.Cfg.Now
		if now == nil {
			now = time.Now
		}
		mcp.BindLeaderInfo(d.Board, leaderInfo{
			d:        d,
			forecast: forecast.NewService(forecast.StoreSource{Store: d.Store}, forecast.Options{}, now),
			report:   report.NewService(report.StoreSource{Store: d.Store}, now),
			search:   search.New(d.Store, d.Log),
			feed:     decisions.New(decisions.StoreSource{Store: d.Store}, nil, decisions.Options{Now: now}),
		})
	})
}

type leaderInfo struct {
	d        Deps
	forecast *forecast.Service
	report   *report.Service
	search   *search.Service
	feed     *decisions.Feed
}

func (l leaderInfo) Forecast(ctx context.Context, briefID string) (any, error) {
	return l.forecast.Forecast(ctx, briefID)
}

func (l leaderInfo) Report(ctx context.Context, briefID string) (report.Report, error) {
	return l.report.Report(ctx, briefID)
}

func (l leaderInfo) Search(ctx context.Context, q, kind string, limit int) ([]search.Result, error) {
	return l.search.Search(ctx, q, kind, limit)
}

func (l leaderInfo) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	return l.feed.List(ctx, 0)
}

func (l leaderInfo) Components(ctx context.Context, briefID string) ([]components.Component, error) {
	if _, err := l.d.Board.GetBrief(ctx, briefID); err != nil {
		return nil, err
	}
	return l.d.Components.List(ctx, briefID)
}
