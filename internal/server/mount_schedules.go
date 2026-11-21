package server

import (
	"context"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/schedule"
)

func init() { registerMount(mountSchedules) }

func mountSchedules(d Deps) {
	exec := &schedule.BoardExecutor{
		Board: d.Board,
		Plan: func(ctx context.Context, briefID string, specs []components.Spec) ([]components.Component, error) {
			return putComponents(ctx, d, briefID, specs)
		},
		Tasks: prompts.New(d.Cfg.PromptsDir),
	}
	svc := schedule.NewService(d.Store, exec, d.Cfg.Now)
	h := schedule.Handler(svc)
	d.API.Mount(d.Mux, "POST /api/v1/schedules", h)
	d.API.Mount(d.Mux, "GET /api/v1/schedules", h)
	d.API.Mount(d.Mux, "GET /api/v1/schedules/preview", h)
	d.API.Mount(d.Mux, "GET /api/v1/schedules/{id}", h)
	d.API.Mount(d.Mux, "PATCH /api/v1/schedules/{id}", h)
	d.API.Mount(d.Mux, "DELETE /api/v1/schedules/{id}", h)
	d.API.Mount(d.Mux, "GET /api/v1/schedules/{id}/runs", h)
	runner := schedule.NewRunner(schedule.Options{
		Store: d.Store, Exec: exec, Now: d.Cfg.Now, Log: d.Log,
		Interval: envDuration(d.Log, "SUBSPACE_SCHEDULE_INTERVAL", schedule.DefaultInterval),
		Grace:    envDuration(d.Log, "SUBSPACE_SCHEDULE_GRACE", schedule.DefaultGrace),
	})
	go runner.Run(d.Ctx)
}
