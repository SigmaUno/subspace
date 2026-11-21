package digest

import (
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/httpx"
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
				httpx.WriteError(w, model.BadRequest("window must be a duration above 0 and up to %s", maxPreviewWindow))
				return
			}
			window = d
		}
		until := now()
		snap, err := src.Snapshot(r.Context(), until.Add(-window), until)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		d := Build(snap)
		httpx.WriteJSON(w, http.StatusOK, Preview{Digest: d, Title: d.Title(), Text: d.Text()})
	})
	return mux
}
