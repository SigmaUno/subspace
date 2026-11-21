package transcript

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		o, err := options(r)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		p, err := s.Read(r.Context(), r.PathValue("id"), o)
		httpx.Reply(w, http.StatusOK, p, err)
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
			httpx.WriteError(w, err)
		}
	})
	return mux
}

func options(r *http.Request) (ReadOptions, error) {
	q := r.URL.Query()
	var o ReadOptions
	var err error
	if o.Since, err = httpx.NonNegInt64(r, "since"); err != nil {
		return o, err
	}
	if o.Limit, err = httpx.PositiveLimit(r, 0, MaxLimit); err != nil {
		return o, err
	}
	o.Query = q.Get("q")
	if at := q.Get("at"); at != "" {
		if o.At, err = time.Parse(time.RFC3339, at); err != nil {
			return o, model.BadRequest("at must be an RFC 3339 timestamp")
		}
	}
	return o, nil
}
