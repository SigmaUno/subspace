package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/agentstats"
	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	autopilotKVPrefix        = "autopilot.brief."
	autopilotDefaultInterval = 5 * time.Second
)

func init() {
	registerMount(func(d Deps) {
		if !strings.EqualFold(os.Getenv("SUBSPACE_AUTOPILOT"), "on") {
			return
		}
		interval := autopilotDefaultInterval
		if v := os.Getenv("SUBSPACE_AUTOPILOT_INTERVAL"); v != "" {
			if p, err := time.ParseDuration(v); err == nil && p > 0 {
				interval = p
			} else {
				d.Log.Warn("invalid SUBSPACE_AUTOPILOT_INTERVAL, using default", "value", v, "default", interval)
			}
		}
		stats := agentstats.NewService(agentstats.StoreSource{Store: d.Store}, time.Now)
		ap := autopilot.New(d.Board, autopilot.Options{Log: d.Log, Scores: stats.Scores, Breaker: d.Breakers.State})
		restoreAutopilotFlags(d, ap)
		h := persistAutopilotFlags(d, ap, autopilot.Handler(ap))
		d.API.Mount(d.Mux, "/api/v1/autopilot", h)
		d.API.Mount(d.Mux, "/api/v1/autopilot/", h)
		go ap.Run(d.Ctx, interval)
	})
}

func restoreAutopilotFlags(d Deps, ap *autopilot.Service) {
	briefs, err := d.Board.ListBriefs(d.Ctx, "")
	if err != nil {
		d.Log.Error("autopilot restore flags", "err", err)
		return
	}
	err = d.Store.View(d.Ctx, func(tx store.Tx) error {
		for _, b := range briefs {
			v, ok, err := tx.GetKV(autopilotKVPrefix + b.ID)
			if err != nil {
				return err
			}
			if ok && v == "true" {
				ap.SetEnabled(b.ID, true)
			}
		}
		return nil
	})
	if err != nil {
		d.Log.Error("autopilot restore flags", "err", err)
	}
}

func persistAutopilotFlags(d Deps, ap *autopilot.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		id := strings.TrimPrefix(r.URL.Path, "/autopilot/briefs/")
		if r.Method != http.MethodPost || rec.code != http.StatusOK || id == r.URL.Path || id == "" {
			return
		}
		err := d.Store.Atomic(r.Context(), func(tx store.Tx) error {
			return tx.SetKV(autopilotKVPrefix+id, strconv.FormatBool(ap.Enabled(id)))
		})
		if err != nil {
			d.Log.Error("autopilot persist flag", "brief", id, "err", err)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
