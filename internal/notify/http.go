package notify

import (
	"encoding/json"
	"net/http"
)

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /notify/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Enabled bool     `json:"enabled"`
			Sinks   []string `json:"sinks"`
			Stats
		}{s.Enabled(), s.SinkNames(), s.Stats()})
	})
	return mux
}
