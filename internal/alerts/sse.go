package alerts

import (
	"bytes"
	"net/http"
	"sync"
	"sync/atomic"
)

const eventsPath = "/api/v1/events"

var laggedMark = []byte("event: lagged\n")

type SSEMonitor struct {
	active atomic.Int64
	mu     sync.Mutex
	lagged map[*sseStream]struct{}
}

func (m *SSEMonitor) Take() (streams, lagged int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lagged = len(m.lagged)
	clear(m.lagged)
	return int(m.active.Load()), lagged
}

func (m *SSEMonitor) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != eventsPath {
			next.ServeHTTP(w, r)
			return
		}
		m.active.Add(1)
		defer m.active.Add(-1)
		next.ServeHTTP(&sseStream{ResponseWriter: w, m: m}, r)
	})
}

type sseStream struct {
	http.ResponseWriter
	m *SSEMonitor
}

func (s *sseStream) Write(p []byte) (int, error) {
	if bytes.Contains(p, laggedMark) {
		s.m.mu.Lock()
		if s.m.lagged == nil {
			s.m.lagged = map[*sseStream]struct{}{}
		}
		s.m.lagged[s] = struct{}{}
		s.m.mu.Unlock()
	}
	return s.ResponseWriter.Write(p)
}

func (s *sseStream) Unwrap() http.ResponseWriter { return s.ResponseWriter }
