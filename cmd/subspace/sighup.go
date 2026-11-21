package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SigmaUno/subspace/internal/reload"
)

type reloader interface {
	Reload(ctx context.Context, note string) (reload.Result, error)
}

func reloadOnSIGHUP(ctx context.Context, r reloader, log *slog.Logger) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		for {
			select {
			case <-sigs:
				reloadOnce(ctx, r, log)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		signal.Stop(sigs)
		close(done)
		<-exited
	}
}

func reloadOnce(ctx context.Context, r reloader, log *slog.Logger) {
	res, err := r.Reload(ctx, "sighup")
	var rejected *reload.Error
	switch {
	case errors.As(err, &rejected):
		log.Error("config reload rejected, keeping current configuration", "errors", rejected.Errors)
	case err != nil:
		log.Error("config reload failed", "err", err)
	default:
		log.Info("config reloaded", "applied", res.Applied, "ignored", res.Ignored)
	}
}
