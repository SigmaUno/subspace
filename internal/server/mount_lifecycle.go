package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

type spawnLifecycleStore interface {
	SpawnLifecycles(ctx context.Context) (map[string]store.SpawnLifecycle, error)
	SaveSpawnLifecycle(ctx context.Context, l store.SpawnLifecycle, sp *model.Spawn, ev *model.AuditEvent) error
}

type lifecycleRecords struct{ s spawnLifecycleStore }

func (l lifecycleRecords) Records(ctx context.Context) (map[string]lifecycle.Record, error) {
	m, err := l.s.SpawnLifecycles(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]lifecycle.Record, len(m))
	for id, r := range m {
		out[id] = lifecycle.Record(r)
	}
	return out, nil
}

func (l lifecycleRecords) Save(ctx context.Context, rec lifecycle.Record, sp *model.Spawn, ev *model.AuditEvent) error {
	return l.s.SaveSpawnLifecycle(ctx, store.SpawnLifecycle(rec), sp, ev)
}

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		st, ok := d.Store.(spawnLifecycleStore)
		if !ok {
			d.Log.Error("spawn lifecycle disabled", "err", "store does not support spawn lifecycle")
			return next
		}
		sp := d.Cfg.Spawner
		if sp == nil {
			sp = spawner.Noop{Log: d.Log}
		}
		r := lifecycle.New(lifecycle.Options{
			Config: lifecycle.Config{
				ConnectTimeout: envDuration(d.Log, "SUBSPACE_SPAWN_CONNECT_TIMEOUT", lifecycle.DefaultConnectTimeout),
				IdleTTL:        envDuration(d.Log, "SUBSPACE_SPAWN_IDLE_TTL", lifecycle.DefaultIdleTTL),
				PauseGrace:     envDuration(d.Log, "SUBSPACE_SPAWN_PAUSE_GRACE", 0),
				Interval:       envDuration(d.Log, "SUBSPACE_SPAWN_REAP_INTERVAL", lifecycle.DefaultInterval),
			},
			Board: d.Board, Store: lifecycleRecords{st}, Spawner: sp, Hub: d.Hub, Log: d.Log,
		})
		h := &lifecycleHandler{r: r, log: d.Log}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /spawns", h.list)
		mux.HandleFunc("POST /spawns/{id}/stop", h.stop)
		d.API.Mount(d.Mux, "POST /api/v1/spawns/{id}/stop", mux)
		listing := http.NewServeMux()
		d.API.Mount(listing, "GET /api/v1/spawns", mux)
		go r.Run(d.Ctx)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/api/v1/spawns" && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
				listing.ServeHTTP(w, req)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
}

func envDuration(log *slog.Logger, key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if v == "0" || v == "off" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Warn("invalid duration, using default", "env", key, "default", def)
		return def
	}
	return d
}

type lifecycleHandler struct {
	r   *lifecycle.Reaper
	log *slog.Logger
}

func (h *lifecycleHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sps, next, err := h.r.List(r.Context(), q.Get("brief_id"), q.Get("cursor"), httpx.Limit(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.Page("spawns", sps, next))
}

func (h *lifecycleHandler) stop(w http.ResponseWriter, r *http.Request) {
	sp, err := h.r.Stop(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sp)
}

func (h *lifecycleHandler) fail(w http.ResponseWriter, err error) {
	httpx.Fail(w, h.log, "spawn lifecycle", err)
}
