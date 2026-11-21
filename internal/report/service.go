package report

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	taskLimit  = 100000
	auditPage  = 1000
	agentLimit = 100000
)

type Source interface {
	Snapshot(ctx context.Context, briefID string) (Snapshot, error)
}

type Service struct {
	src Source
	now func() time.Time
}

func NewService(src Source, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{src: src, now: now}
}

func (s *Service) Report(ctx context.Context, briefID string) (Report, error) {
	snap, err := s.src.Snapshot(ctx, briefID)
	if err != nil {
		return Report{}, err
	}
	snap.Now = s.now()
	return Build(snap), nil
}

type StoreSource struct {
	Store store.Store
}

type prLister interface {
	PRStatuses(ctx context.Context) ([][]byte, error)
}

func (s StoreSource) prs(ctx context.Context, briefID string) (map[string]PR, error) {
	l, ok := s.Store.(prLister)
	if !ok {
		return nil, nil
	}
	raws, err := l.PRStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]PR{}
	for _, raw := range raws {
		var st struct {
			TaskID  string `json:"task_id"`
			BriefID string `json:"brief_id"`
			State   string `json:"state"`
			Diff    *struct {
				Additions int `json:"additions"`
				Deletions int `json:"deletions"`
			} `json:"diff"`
		}
		if json.Unmarshal(raw, &st) != nil || st.BriefID != briefID {
			continue
		}
		pr := PR{State: st.State}
		if st.Diff != nil {
			pr.Additions, pr.Deletions, pr.HasDiff = st.Diff.Additions, st.Diff.Deletions, true
		}
		out[st.TaskID] = pr
	}
	return out, nil
}

func (s StoreSource) Snapshot(ctx context.Context, briefID string) (Snapshot, error) {
	var out Snapshot
	err := s.Store.View(ctx, func(tx store.Tx) error {
		b, err := tx.GetBrief(briefID)
		if err != nil {
			return err
		}
		out.Brief = *b
		if out.Brief.CostUsed, err = tx.BriefCost(briefID); err != nil {
			return err
		}
		if out.Tasks, err = tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: taskLimit}); err != nil {
			return err
		}
		agents, err := tx.ListAgents(store.AgentFilter{Limit: agentLimit})
		if err != nil {
			return err
		}
		out.Agents = make(map[string]string, len(agents))
		for _, a := range agents {
			out.Agents[a.ID] = a.Provider
		}
		for _, t := range out.Tasks {
			var since int64
			for {
				page, err := tx.ListAudit(store.AuditFilter{TaskID: t.ID, SinceID: since, Limit: auditPage})
				if err != nil {
					return err
				}
				out.Audit = append(out.Audit, page...)
				if len(page) < auditPage {
					break
				}
				since = page[len(page)-1].ID
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, model.NotFound("brief %s not found", briefID)
	}
	if err != nil {
		return out, err
	}
	out.PRs, err = s.prs(ctx, briefID)
	return out, err
}
