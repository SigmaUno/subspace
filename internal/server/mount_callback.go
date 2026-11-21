package server

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/callback"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	callbackAllowPrivateEnv = "SUBSPACE_CALLBACK_ALLOW_PRIVATE"
	callbackCoalesceEnv     = "SUBSPACE_CALLBACK_COALESCE"
	callbackBackoffEnv      = "SUBSPACE_CALLBACK_BACKOFF"
	callbackTimeout         = 10 * time.Second
)

var callbackRegistrars sync.Map

func init() {
	registerMount(mountCallbacks)
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		v, ok := callbackRegistrars.Load(d.Mux)
		if !ok {
			return next
		}
		return v.(*callback.Registrar).Wrap(next)
	})
}

func mountCallbacks(d Deps) {
	if !store.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("agent callbacks disabled", "err", "store does not support SQL extensions")
		return
	}
	allow, _ := strconv.ParseBool(os.Getenv(callbackAllowPrivateEnv))
	policy := callback.Policy{AllowPrivate: allow}
	targets := callback.SQLStore{St: d.Store}
	disp := callback.NewDispatcher(callback.Options{
		Targets:  targets,
		Client:   policy.NewClient(callbackTimeout),
		Log:      d.Log,
		Coalesce: callbackDuration(d, callbackCoalesceEnv),
		Backoff:  callbackDuration(d, callbackBackoffEnv),
		Timeout:  callbackTimeout,
	})
	w := &callback.Watcher{D: disp, Tasks: callbackOwners{d.Board}}
	activate := func() { w.Activate(d.Ctx, d.Leader.Gate(d.Hub)) }
	if has, err := targets.Any(d.Ctx); err != nil {
		d.Log.Error("agent callbacks: reading existing callbacks", "err", err)
	} else if has {
		activate()
	}
	callbackRegistrars.Store(d.Mux, &callback.Registrar{Policy: policy, Store: targets, OwnerToken: d.Cfg.OwnerToken, Log: d.Log, Activated: activate})
	go disp.Run(d.Ctx)
	context.AfterFunc(d.Ctx, func() {
		callbackRegistrars.Delete(d.Mux)
	})
}

func callbackDuration(d Deps, env string) time.Duration {
	v := os.Getenv(env)
	if v == "" {
		return 0
	}
	dur, err := time.ParseDuration(v)
	if err != nil || dur <= 0 {
		d.Log.Error("invalid duration, using default", "env", env)
		return 0
	}
	return dur
}

type callbackOwners struct{ b *board.Board }

func (o callbackOwners) Owners(ctx context.Context, taskID string) (string, string, bool) {
	t, err := o.b.GetTask(ctx, model.Owner, taskID)
	if err != nil {
		return "", "", false
	}
	var assignee, reviewer string
	if t.Assignee != nil {
		assignee = *t.Assignee
	}
	if t.Reviewer != nil {
		reviewer = *t.Reviewer
	}
	return assignee, reviewer, true
}
