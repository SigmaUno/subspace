package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SigmaUno/subspace/internal/httpx"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	spendguardInterval = 10 * time.Second
	spendguardTaskMax  = 100000
	spendguardBodyMax  = 64 << 10
)

var spendGuards sync.Map

func init() {
	registerMount(mountSpendGuard)
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		v, ok := spendGuards.Load(d.Mux)
		if !ok {
			return next
		}
		return spendCostReports(next, d, v.(*spendguard.Guard))
	})
}

func mountSpendGuard(d Deps) {
	g, err := spendguard.New(d.Ctx, spendguard.Options{
		Tasks:    spendTasks{d.Store},
		Usage:    d.Usage,
		EURToUSD: d.UsageSetup.EURToUSD,
		Control:  d.Board,
		KV:       spendKV{d.Store},
		Pub:      d.Hub,
		Audit: func(ctx context.Context, before, after spendguard.Limits) error {
			return d.Store.Atomic(ctx, func(tx store.Tx) error {
				return tx.InsertAudit(&model.AuditEvent{
					TS: time.Now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: "set_limits",
					Before: map[string]any{"daily_cost_eur": before.DailyCostEUR, "weekly_cost_eur": before.WeeklyCostEUR},
					After:  map[string]any{"daily_cost_eur": after.DailyCostEUR, "weekly_cost_eur": after.WeeklyCostEUR},
				})
			})
		},
		Log: d.Log,
	})
	if err != nil {
		d.Log.Error("spendguard disabled", "err", err)
		return
	}
	shared := isPostgres(d.Cfg.DBPath)
	h := spendguard.Handler(g)
	if shared {
		h = reloadingGuard(h, d, g)
	}
	d.API.Mount(d.Mux, "GET /api/v1/limits", h)
	d.API.Mount(d.Mux, "PUT /api/v1/limits", h)
	detach := decisions.Attach(d.Store, g)
	spendGuards.Store(d.Mux, g)
	go func() {
		<-d.Ctx.Done()
		detach()
		spendGuards.Delete(d.Mux)
	}()
	d.Leader.Go(d.Ctx, func(ctx context.Context) { runSpendGuard(ctx, d, g, shared) })
}

func runSpendGuard(ctx context.Context, d Deps, g *spendguard.Guard, shared bool) {
	tick := time.NewTicker(spendguardInterval)
	defer tick.Stop()
	for {
		var err error
		if shared {
			err = g.Reload(ctx)
		}
		if err == nil {
			err = g.Reconcile(ctx)
		}
		if err != nil && ctx.Err() == nil {
			d.Log.Error("spendguard sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func reloadingGuard(next http.Handler, d Deps, g *spendguard.Guard) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.Reload(r.Context()); err != nil {
			d.Log.Error("spendguard reload", "err", err)
			httpx.WriteError(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type readCloser struct {
	io.Reader
	io.Closer
}

func spendCostReports(next http.Handler, d Deps, g *spendguard.Guard) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/mcp" {
			next.ServeHTTP(w, r)
			return
		}
		head, _ := io.ReadAll(io.LimitReader(r.Body, spendguardBodyMax+1))
		r.Body = readCloser{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		if sw.code != http.StatusOK || len(head) > spendguardBodyMax || !bytes.Contains(head, []byte("update_status")) {
			return
		}
		var call struct {
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					TaskID string `json:"task_id"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(head, &call) != nil || call.Params.Name != "update_status" || call.Params.Arguments.TaskID == "" {
			return
		}
		if !d.Leader.IsLeader() {
			return
		}
		go func() {
			var err error
			if isPostgres(d.Cfg.DBPath) {
				err = g.Reload(d.Ctx)
			}
			if err == nil {
				err = g.CheckTask(d.Ctx, call.Params.Arguments.TaskID)
			}
			if err != nil && d.Ctx.Err() == nil {
				d.Log.Error("spendguard cost report", "task", call.Params.Arguments.TaskID, "err", err)
			}
		}()
	})
}

type statusWriter struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.code, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type spendTasks struct{ st store.Store }

func (s spendTasks) Tasks(ctx context.Context) ([]spendguard.TaskCost, error) {
	var out []spendguard.TaskCost
	err := s.st.View(ctx, func(tx store.Tx) error {
		ts, err := tx.ListTasks(store.TaskFilter{Limit: spendguardTaskMax})
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.CostUsed > 0 {
				out = append(out, spendguard.TaskCost{ID: t.ID, CostEUR: t.CostUsed, UpdatedAt: t.UpdatedAt})
			}
		}
		return nil
	})
	return out, err
}

func (s spendTasks) Task(ctx context.Context, id string) (spendguard.TaskCost, error) {
	var out spendguard.TaskCost
	err := s.st.View(ctx, func(tx store.Tx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		out = spendguard.TaskCost{ID: t.ID, CostEUR: t.CostUsed, UpdatedAt: t.UpdatedAt}
		return nil
	})
	return out, err
}

type spendKV struct{ st store.Store }

func (s spendKV) Get(ctx context.Context, key string) (v string, ok bool, err error) {
	err = s.st.View(ctx, func(tx store.Tx) (e error) { v, ok, e = tx.GetKV(key); return })
	return
}

func (s spendKV) Set(ctx context.Context, key, value string) error {
	return s.st.Atomic(ctx, func(tx store.Tx) error { return tx.SetKV(key, value) })
}
