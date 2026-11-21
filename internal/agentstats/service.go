package agentstats

import (
	"context"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	taskLimit  = 100000
	agentLimit = 100000
	auditPage  = 1000
	scoreTTL   = 30 * time.Second
)

type Source interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

type UsageSource interface {
	Query(ctx context.Context, q usage.Query) ([]usage.Record, error)
}

type Service struct {
	src Source
	now func() time.Time

	mu     sync.Mutex
	scores map[string]float64
	at     time.Time
}

func NewService(src Source, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{src: src, now: now}
}

func (s *Service) build(ctx context.Context) ([]Stats, error) {
	snap, err := s.src.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	snap.Now = s.now()
	return Build(snap), nil
}

func (s *Service) All(ctx context.Context, role string) ([]Stats, error) {
	all, err := s.build(ctx)
	if err != nil || role == "" {
		return all, err
	}
	out := all[:0]
	for _, st := range all {
		if st.Role == role {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *Service) Agent(ctx context.Context, id string) (Stats, error) {
	all, err := s.build(ctx)
	if err != nil {
		return Stats{}, err
	}
	for _, st := range all {
		if st.AgentID == id {
			return st, nil
		}
	}
	return Stats{}, model.NotFound("agent %s not found", id)
}

func (s *Service) Scores(ctx context.Context) (map[string]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scores != nil && s.now().Sub(s.at) < scoreTTL {
		return s.scores, nil
	}
	all, err := s.build(ctx)
	if err != nil {
		return nil, err
	}
	scores := make(map[string]float64, len(all))
	for _, st := range all {
		scores[st.AgentID] = st.Score
	}
	s.scores, s.at = scores, s.now()
	return scores, nil
}

type StoreSource struct {
	Store store.Store
	Usage UsageSource
}

func (s StoreSource) Snapshot(ctx context.Context) (Snapshot, error) {
	var out Snapshot
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		if out.Agents, err = tx.ListAgents(store.AgentFilter{Limit: agentLimit}); err != nil {
			return err
		}
		if out.Tasks, err = tx.ListTasks(store.TaskFilter{Limit: taskLimit}); err != nil {
			return err
		}
		var since int64
		for {
			page, err := tx.ListAudit(store.AuditFilter{SinceID: since, Limit: auditPage})
			if err != nil {
				return err
			}
			out.Audit = append(out.Audit, page...)
			if len(page) < auditPage {
				return nil
			}
			since = page[len(page)-1].ID
		}
	})
	if err != nil {
		return Snapshot{}, err
	}
	if s.Usage != nil {
		if out.Usage, err = s.Usage.Query(ctx, usage.Query{}); err != nil {
			return Snapshot{}, err
		}
	}
	return out, nil
}
