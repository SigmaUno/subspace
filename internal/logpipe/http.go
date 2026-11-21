package logpipe

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	defaultLimit = 500
	maxBody      = 1 << 10
)

type Audit func(ctx context.Context, before, after string) error

func LogsHandler(p *Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := parseQuery(r)
		if err != nil {
			fail(w, err)
			return
		}
		lines := p.Lines(q)
		if lines == nil {
			lines = []Line{}
		}
		write(w, http.StatusOK, map[string]any{"lines": lines})
	})
}

func LevelHandler(p *Pipeline, audit Audit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			write(w, http.StatusOK, map[string]string{"level": LevelName(p.Level())})
			return
		}
		if err := requireOwner(r); err != nil {
			fail(w, err)
			return
		}
		var in struct {
			Level string `json:"level"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, model.BadRequest("invalid body: %v", err))
			return
		}
		level, err := ParseLevel(in.Level)
		if err != nil || in.Level == "" {
			fail(w, model.BadRequest("level must be debug, info, warn or error"))
			return
		}
		before := LevelName(p.Level())
		after := LevelName(level)
		if audit != nil && before != after {
			if err := audit(r.Context(), before, after); err != nil {
				fail(w, err)
				return
			}
		}
		p.SetLevel(level)
		write(w, http.StatusOK, map[string]string{"level": after})
	})
}

func requireOwner(r *http.Request) error {
	if pr, ok := auth.PrincipalFrom(r.Context()); !ok || pr.Scope != auth.ScopeOwner {
		return model.Forbidden("owner scope required")
	}
	return nil
}

func parseQuery(r *http.Request) (Query, error) {
	v := r.URL.Query()
	q := Query{Min: slog.LevelDebug, Limit: defaultLimit}
	if s := v.Get("level"); s != "" {
		l, err := ParseLevel(s)
		if err != nil {
			return q, model.BadRequest("level must be debug, info, warn or error")
		}
		q.Min = l
	}
	if s := v.Get("since"); s != "" {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id >= 0 {
			q.SinceID = id
		} else if ts, err := time.Parse(time.RFC3339, s); err == nil {
			q.SinceTS = ts
		} else {
			return q, model.BadRequest("since must be a line id or an RFC 3339 timestamp")
		}
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return q, model.BadRequest("limit must be a positive integer")
		}
		q.Limit = min(n, DefaultCapacity)
	}
	return q, nil
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
