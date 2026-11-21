package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/agentstats"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	autopilotKVPrefix        = "autopilot.brief."
	autopilotSpawnKVPrefix   = "autopilot.spawn."
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
		profs := &profiles.Store{St: d.Store, Now: d.Cfg.Now}
		ap := autopilot.New(d.Board, autopilot.Options{Log: d.Log, Scores: stats.Scores, Breaker: d.Breakers.State,
			Profiles: func(ctx context.Context, name string) (autopilot.SpawnConfig, error) {
				p, err := profs.Get(ctx, name)
				return autopilot.SpawnConfig{Kind: p.Kind, Model: p.Model, Provider: p.Provider}, err
			}})
		restoreAutopilotFlags(d, ap)
		h := persistAutopilotSpawn(d, ap, persistAutopilotFlags(d, ap, autopilot.Handler(ap)))
		d.API.Mount(d.Mux, "/api/v1/autopilot", h)
		d.API.Mount(d.Mux, "/api/v1/autopilot/", h)
		d.Leader.Go(d.Ctx, func(ctx context.Context) {
			if isPostgres(d.Cfg.DBPath) {
				go syncAutopilotFlags(ctx, d, ap, interval)
			}
			ap.Run(ctx, interval)
		})
	})
}

func syncAutopilotFlags(ctx context.Context, d Deps, ap *autopilot.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := loadAutopilotFlags(ctx, d, ap)
		if err != nil && ctx.Err() == nil {
			d.Log.Error("autopilot sync flags", "err", err)
		}
	}
}

func restoreAutopilotFlags(d Deps, ap *autopilot.Service) {
	if err := loadAutopilotFlags(d.Ctx, d, ap); err != nil {
		d.Log.Error("autopilot restore flags", "err", err)
	}
}

func loadAutopilotFlags(ctx context.Context, d Deps, ap *autopilot.Service) error {
	briefs, err := d.Board.ListBriefs(ctx, "")
	if err != nil {
		return err
	}
	return d.Store.View(ctx, func(tx store.Tx) error {
		for _, b := range briefs {
			v, _, err := tx.GetKV(autopilotKVPrefix + b.ID)
			if err != nil {
				return err
			}
			ap.SetEnabled(b.ID, v == "true")
			raw, _, err := tx.GetKV(autopilotSpawnKVPrefix + b.ID)
			if err != nil {
				return err
			}
			var c autopilot.SpawnConfig
			if raw == "" || json.Unmarshal([]byte(raw), &c) == nil {
				ap.SetSpawn(b.ID, c)
			}
		}
		return nil
	})
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

func persistAutopilotSpawn(d Deps, ap *autopilot.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		before := ""
		id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/autopilot/briefs/"), "/spawn")
		isSet := ok && r.Method == http.MethodPut && id != "" && !strings.Contains(id, "/")
		if isSet {
			before = spawnJSON(ap.Spawn(id))
		}
		next.ServeHTTP(rec, r)
		if !isSet || rec.code != http.StatusOK {
			return
		}
		after := spawnJSON(ap.Spawn(id))
		err := d.Store.Atomic(r.Context(), func(tx store.Tx) error {
			if err := tx.SetKV(autopilotSpawnKVPrefix+id, after); err != nil {
				return err
			}
			note := auth.AuditNote(r.Context(), "brief "+id)
			return tx.InsertAudit(&model.AuditEvent{
				TS: time.Now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: "set_autopilot_spawn",
				Before: map[string]any{"brief_id": id, "spawn": before}, After: map[string]any{"brief_id": id, "spawn": after}, Note: &note,
			})
		})
		if err != nil {
			d.Log.Error("autopilot persist spawn", "brief", id, "err", err)
		}
	})
}

func spawnJSON(c autopilot.SpawnConfig) string {
	if c == (autopilot.SpawnConfig{}) {
		return ""
	}
	b, _ := json.Marshal(c)
	return string(b)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
