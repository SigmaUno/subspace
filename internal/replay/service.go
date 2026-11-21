package replay

import (
	"context"
	"errors"
	"sync"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	auditPage  = 1000
	listLimit  = 100000
	maxCached  = 32
	cacheEvery = DefaultEvery
)

type Service struct {
	st store.Store

	mu      sync.Mutex
	briefs  map[string]*History
	used    map[string]uint64
	counter uint64
}

func NewService(st store.Store) *Service {
	return &Service{st: st, briefs: map[string]*History{}, used: map[string]uint64{}}
}

func (s *Service) with(ctx context.Context, briefID string, fn func(*History) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.briefs[briefID]
	if !ok {
		h = NewHistory(briefID, cacheEvery)
	}
	err := s.st.View(ctx, func(tx store.Tx) error {
		if _, err := tx.GetBrief(briefID); err != nil {
			return err
		}
		var events []*model.AuditEvent
		for since := h.Scanned(); ; {
			page, err := tx.ListAudit(store.AuditFilter{SinceID: since, Limit: auditPage})
			if err != nil {
				return err
			}
			events = append(events, page...)
			if len(page) < auditPage {
				break
			}
			since = page[len(page)-1].ID
		}
		tasks, err := tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: listLimit})
		if err != nil {
			return err
		}
		agents, err := tx.ListAgents(store.AgentFilter{Limit: listLimit})
		if err != nil {
			return err
		}
		roles := make(map[string]string, len(agents))
		for _, a := range agents {
			roles[a.ID] = a.Role
		}
		h.Update(tasks, roles)
		h.Append(events)
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return model.NotFound("brief %s not found", briefID)
	}
	if err != nil {
		return err
	}
	s.counter++
	s.briefs[briefID], s.used[briefID] = h, s.counter
	s.evict()
	return fn(h)
}

func (s *Service) evict() {
	for len(s.briefs) > maxCached {
		oldest, at := "", ^uint64(0)
		for id, n := range s.used {
			if n < at {
				oldest, at = id, n
			}
		}
		delete(s.briefs, oldest)
		delete(s.used, oldest)
	}
}

func (s *Service) Snapshot(ctx context.Context, briefID, at string) (snap Snapshot, err error) {
	err = s.with(ctx, briefID, func(h *History) error {
		n, err := h.Resolve(at)
		if err != nil {
			return err
		}
		snap = h.Snapshot(n)
		return nil
	})
	return snap, err
}

func (s *Service) Timeline(ctx context.Context, briefID string) (tl Timeline, err error) {
	err = s.with(ctx, briefID, func(h *History) error {
		tl = h.Timeline()
		return nil
	})
	return tl, err
}

func (s *Service) Diff(ctx context.Context, briefID, from, to string) (d Diff, err error) {
	err = s.with(ctx, briefID, func(h *History) error {
		if from == "" {
			from = "0"
		}
		f, err := h.Resolve(from)
		if err != nil {
			return err
		}
		t, err := h.Resolve(to)
		if err != nil {
			return err
		}
		if f > t {
			return model.BadRequest("from must not be after to")
		}
		d = h.Diff(f, t)
		return nil
	})
	return d, err
}
