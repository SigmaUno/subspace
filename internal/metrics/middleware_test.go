package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareCountsLaggedFramesAndSkipsStreamLatency(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: lagged\ndata: {\"type\":\"lagged\",\"ts\":\"t\",\"data\":{\"dropped\":7}}\n\n"))
		_, _ = w.Write([]byte("event: lagged\ndata: {\"type\":\"lagged\",\"ts\":\"t\",\"data\":{\"dropped\":3}}\n\n"))
		_, _ = w.Write([]byte(": ping\n\n"))
	})
	mux.HandleFunc("GET /api/v1/things/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := s.Middleware(mux, Options{})

	for _, path := range []string{"/api/v1/events", "/api/v1/things/1", "/api/v1/things/2", "/nowhere"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}

	p := parseText(t, scrape(t, s.Registry))
	want := map[string]float64{
		`subspace_sse_dropped_events_total`:                                             10,
		`subspace_http_requests_total{code="200",route="GET /api/v1/events"}`:           1,
		`subspace_http_requests_total{code="418",route="GET /api/v1/things/{id}"}`:      2,
		`subspace_http_requests_total{code="404",route="unmatched"}`:                    1,
		`subspace_http_request_duration_seconds_count{route="GET /api/v1/things/{id}"}`: 2,
	}
	for k, v := range want {
		if got, ok := p.samples[k]; !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
	if _, ok := p.samples[`subspace_http_request_duration_seconds_count{route="GET /api/v1/events"}`]; ok {
		t.Error("event stream must not be timed")
	}
}
