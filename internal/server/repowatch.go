package server

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/repos"
)

func waitRepoChange(ctx context.Context, sub events.Subscription) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-sub.C():
			if !ok {
				return false
			}
			if ev.Type != repos.ChangedEvent && sub.TakeDropped() == 0 {
				continue
			}
			for drained := false; !drained; {
				select {
				case <-sub.C():
				default:
					drained = true
				}
			}
			return true
		}
	}
}

func watchRepos(ctx context.Context, d Deps, reload func()) {
	sub := events.SubscribeQuiet(d.Hub, []string{"control"})
	go func() {
		defer sub.Close()
		for waitRepoChange(ctx, sub) {
			reload()
		}
	}()
}

func superviseRepos(ctx context.Context, d Deps, build func(context.Context) (func(context.Context), error)) {
	sub := events.SubscribeQuiet(d.Hub, []string{"control"})
	defer sub.Close()
	for {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			run, err := build(runCtx)
			if err != nil {
				d.Log.Error("repository registry reload failed", "err", err)
				return
			}
			if run != nil {
				run(runCtx)
			}
		}()
		changed := waitRepoChange(ctx, sub)
		cancel()
		<-done
		if !changed {
			return
		}
	}
}

func resolveRepos(ctx context.Context, d Deps, what string) (prsync.Config, bool, error) {
	cfg, ok, err := prsync.Resolve(ctx, repoSource(d), d.Log)
	if err != nil {
		return prsync.Config{}, false, fmt.Errorf("%s disabled: %w", what, err)
	}
	return cfg, ok, nil
}
