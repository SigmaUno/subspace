package alerts

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/SigmaUno/subspace/internal/events"
)

const eventsPath = "/api/v1/events"

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
		s := &sseStream{m: m}
		next.ServeHTTP(w, r.WithContext(events.WithLaggedHook(r.Context(), s.lagged)))
	})
}

type sseStream struct {
	m *SSEMonitor
}

func (s *sseStream) lagged() {
	s.m.mu.Lock()
	if s.m.lagged == nil {
		s.m.lagged = map[*sseStream]struct{}{}
	}
	s.m.lagged[s] = struct{}{}
	s.m.mu.Unlock()
}
