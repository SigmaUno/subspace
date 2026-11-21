package search

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	MaxQueryRunes = 200
	DefaultLimit  = 20
	MaxLimit      = 100
	contextBytes  = 40
)

const (
	hlOpen  = "«"
	hlClose = "»"
)

var kinds = map[string]bool{"task": true, "message": true, "agent": true, "brief": true}

type Result struct {
	Kind    string    `json:"kind"`
	ID      string    `json:"id"`
	Field   string    `json:"field"`
	Snippet string    `json:"snippet"`
	At      time.Time `json:"at"`
}

type Service struct {
	st  store.Store
	log *slog.Logger
}

func New(st store.Store, log *slog.Logger) *Service {
	return &Service{st: st, log: log}
}

func (s *Service) Search(ctx context.Context, q, kind string, limit int) ([]Result, error) {
	q = strings.TrimSpace(q)
	switch {
	case q == "":
		return nil, model.BadRequest("q is required")
	case utf8.RuneCountInString(q) > MaxQueryRunes:
		return nil, model.BadRequest("q must be at most %d characters", MaxQueryRunes)
	case kind != "" && !kinds[kind]:
		return nil, model.BadRequest("kind must be one of task, message, agent, brief")
	}
	if limit < 1 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	var hits []store.SearchHit
	err := s.st.View(ctx, func(tx store.Tx) error {
		st, ok := tx.(store.SearchTx)
		if !ok {
			return errors.New("store does not support search")
		}
		var err error
		hits, err = st.Search(store.SearchQuery{Text: q, Kind: kind, Limit: limit})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		out = append(out, Result{Kind: h.Kind, ID: h.ID, Field: h.Field, Snippet: snippet(h.Text, q), At: h.At})
	}
	return out, nil
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
			return
		}
		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			s.fail(w, err)
			return
		}
		rs, err := s.Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("kind"), limit)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": rs})
	})
}

func parseLimit(v string) (int, error) {
	if v == "" {
		return DefaultLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, model.BadRequest("limit must be a positive integer")
	}
	return n, nil
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && s.log != nil {
		s.log.Error("search error", "err", err)
	}
	s.writeError(w, e.HTTPStatus(), e.Code, e.Message)
}

func (s *Service) writeError(w http.ResponseWriter, status int, code, msg string) {
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var flatten = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")

func snippet(text, q string) string {
	text = flatten.Replace(text)
	var start, end int
	if i := indexFold(text, q); i >= 0 {
		start, end = i, i+len(q)
	}
	from := max(start-contextBytes, 0)
	for from > 0 && from < start && !utf8.RuneStart(text[from]) {
		from++
	}
	to := min(end+contextBytes, len(text))
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	var b strings.Builder
	if from > 0 {
		b.WriteString("…")
	}
	b.WriteString(text[from:start])
	if end > start {
		b.WriteString(hlOpen + text[start:end] + hlClose)
	}
	b.WriteString(text[end:to])
	if to < len(text) {
		b.WriteString("…")
	}
	return b.String()
}

func indexFold(text, q string) int {
	return strings.Index(asciiLower(text), asciiLower(q))
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
