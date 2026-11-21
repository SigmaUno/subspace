package digest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	defaultPreviewWindow = 24 * time.Hour
	maxPreviewWindow     = 31 * 24 * time.Hour
)

type Preview struct {
	Digest Digest `json:"digest"`
	Title  string `json:"title"`
	Text   string `json:"text"`
}

func Handler(src Source, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /digest/preview", func(w http.ResponseWriter, r *http.Request) {
		window := defaultPreviewWindow
		if v := r.URL.Query().Get("window"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 || d > maxPreviewWindow {
				writeError(w, model.BadRequest("window must be a duration above 0 and up to %s", maxPreviewWindow))
				return
			}
			window = d
		}
		until := now()
		snap, err := src.Snapshot(r.Context(), until.Add(-window), until)
		if err != nil {
			writeError(w, err)
			return
		}
		d := Build(snap)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Preview{Digest: d, Title: d.Title(), Text: d.Text()})
	})
	return mux
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
