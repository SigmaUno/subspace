package transcript

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		o, err := options(r)
		if err != nil {
			fail(w, err)
			return
		}
		p, err := s.Read(r.Context(), r.PathValue("id"), o)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, p)
	})
	mux.HandleFunc("GET /tasks/{id}/transcript/download", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		started := false
		err := s.Download(r.Context(), id, func(size int64) io.Writer {
			started = true
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.ndjson.gz"`)
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
			return w
		})
		switch {
		case err == nil:
		case started:
			panic(http.ErrAbortHandler)
		default:
			fail(w, err)
		}
	})
	return mux
}

func options(r *http.Request) (ReadOptions, error) {
	q := r.URL.Query()
	var o ReadOptions
	var err error
	if o.Since, err = intParam(q.Get("since")); err != nil {
		return o, model.BadRequest("since must be a non-negative integer")
	}
	limit, err := intParam(q.Get("limit"))
	if err != nil || (q.Has("limit") && limit < 1) {
		return o, model.BadRequest("limit must be a positive integer")
	}
	o.Limit = int(min(limit, MaxLimit))
	o.Query = q.Get("q")
	if at := q.Get("at"); at != "" {
		if o.At, err = time.Parse(time.RFC3339, at); err != nil {
			return o, model.BadRequest("at must be an RFC 3339 timestamp")
		}
	}
	return o, nil
}

func intParam(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
