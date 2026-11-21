package server

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/logpipe"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	envLogSample     = "SUBSPACE_LOG_SAMPLE"
	defaultLogSample = 0.1
)

func init() {
	registerMount(mountLogs)
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		return logpipe.AccessLog(next, logpipe.AccessOptions{
			Log:    d.Log,
			Sample: logSample(d),
			Route:  func(r *http.Request) string { _, p := d.Mux.Handler(r); return p },
			Actor:  func(r *http.Request) (string, string) { return requestActor(d, r) },
		})
	})
}

func mountLogs(d Deps) {
	p := d.Cfg.LogPipe
	d.API.Mount(d.Mux, "GET /api/v1/logs", logpipe.LogsHandler(p))
	h := logpipe.LevelHandler(p, func(ctx context.Context, before, after string) error {
		e := &model.AuditEvent{
			TS: time.Now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: "set_log_level",
			Before: map[string]any{"level": before}, After: map[string]any{"level": after},
		}
		if note := auth.AuditNote(ctx, ""); note != "" {
			e.Note = &note
		}
		return d.Store.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAudit(e) })
	})
	d.API.Mount(d.Mux, "GET /api/v1/log-level", h)
	d.API.Mount(d.Mux, "PUT /api/v1/log-level", h)
}

func logSample(d Deps) float64 {
	v := os.Getenv(envLogSample)
	if v == "" {
		return defaultLogSample
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || rate < 0 || rate > 1 {
		d.Log.Warn("invalid log sample rate, using default", "env", envLogSample, "default", defaultLogSample)
		return defaultLogSample
	}
	return rate
}

func requestActor(d Deps, r *http.Request) (agent, scope string) {
	const prefix = "Bearer "
	hd := r.Header.Get("Authorization")
	if len(hd) <= len(prefix) || hd[:len(prefix)] != prefix {
		return "", ""
	}
	tok := hd[len(prefix):]
	switch {
	case r.URL.Path == "/mcp":
		if d.Auth != nil {
			if a, err := d.Auth.LookupToken(tok); err == nil && a != nil {
				return a.ID, ""
			}
		}
	case auth.OwnerMatch(tok, d.Cfg.OwnerToken):
		return "", auth.ScopeOwner
	case strings.HasPrefix(tok, auth.OwnerTokenPrefix):
		return "", "token"
	}
	return "", ""
}
