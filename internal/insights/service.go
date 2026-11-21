package insights

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	listLimit    = 100000
	pageSize     = 1000
	errorLimit   = 100000
	decisionTTL  = 15 * time.Second
	suggestedFix = "Suggested: pause the agent, reassign its tasks or change its instructions."
)

var auditTools = []string{"claim_task", "lease_release", "review"}

type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	High        int       `json:"high"`
	Warning     int       `json:"warning"`
	Findings    []Finding `json:"findings"`
}

type Source interface {
	Snapshot(ctx context.Context, now time.Time, limits Thresholds) (Input, error)
}

type Service struct {
	src    Source
	rec    *Recorder
	now    func() time.Time
	limits Thresholds

	mu     sync.Mutex
	cached []Finding
	at     time.Time
}

func NewService(src Source, rec *Recorder, now func() time.Time, limits Thresholds) *Service {
	if now == nil {
		now = time.Now
	}
	if limits == (Thresholds{}) {
		limits = DefaultThresholds()
	}
	return &Service{src: src, rec: rec, now: now, limits: limits}
}

func (s *Service) findings(ctx context.Context) ([]Finding, error) {
	in, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return Analyze(in), nil
}

func newReport(now time.Time, fs []Finding) Report {
	r := Report{GeneratedAt: now.UTC(), Findings: []Finding{}}
	for _, f := range fs {
		if f.Severity == SeverityHigh {
			r.High++
		} else {
			r.Warning++
		}
		r.Findings = append(r.Findings, f)
	}
	return r
}

func (s *Service) All(ctx context.Context, severity string) (Report, error) {
	fs, err := s.findings(ctx)
	if err != nil {
		return Report{}, err
	}
	if severity != "" {
		kept := fs[:0]
		for _, f := range fs {
			if f.Severity == severity {
				kept = append(kept, f)
			}
		}
		fs = kept
	}
	return newReport(s.now(), fs), nil
}

func (s *Service) Agent(ctx context.Context, id string) (Report, error) {
	in, err := s.snapshot(ctx)
	if err != nil {
		return Report{}, err
	}
	known := false
	for _, a := range in.Agents {
		known = known || a.ID == id
	}
	if !known {
		return Report{}, model.NotFound("agent %s not found", id)
	}
	var mine []Finding
	for _, f := range Analyze(in) {
		if f.AgentID == id {
			mine = append(mine, f)
		}
	}
	return newReport(in.Now, mine), nil
}

func (s *Service) snapshot(ctx context.Context) (Input, error) {
	if s.rec != nil {
		if err := s.rec.Flush(ctx); err != nil {
			return Input{}, err
		}
	}
	now := s.now()
	in, err := s.src.Snapshot(ctx, now, s.limits)
	if err != nil {
		return Input{}, err
	}
	in.Now, in.Limits = now, s.limits
	return in, nil
}

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == nil || s.now().Sub(s.at) >= decisionTTL {
		fs, err := s.findings(ctx)
		if err != nil {
			return nil, err
		}
		s.cached, s.at = append([]Finding{}, fs...), s.now()
	}
	var out []*decisions.Decision
	for _, f := range s.cached {
		if f.Severity != SeverityHigh {
			continue
		}
		out = append(out, &decisions.Decision{
			ID: "insight:" + f.ID, Kind: decisions.KindAlert, AgentID: f.AgentID, CreatedAt: f.Since,
			Title:   "Agent " + f.AgentID + ": " + f.Summary,
			Summary: suggestedFix,
			Links:   decisions.Links{Thread: model.ThreadAgent + ":" + f.AgentID},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type StoreSource struct {
	Store  store.Store
	Errors Store
}

func (s StoreSource) Snapshot(ctx context.Context, now time.Time, limits Thresholds) (Input, error) {
	var in Input
	since := now.Add(-limits.Window)
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		if in.Agents, err = tx.ListAgents(store.AgentFilter{Limit: listLimit}); err != nil {
			return err
		}
		if in.Tasks, err = tx.ListTasks(store.TaskFilter{Limit: listLimit}); err != nil {
			return err
		}
		for _, tool := range auditTools {
			var after int64
			for {
				page, err := tx.ListAudit(store.AuditFilter{Tool: tool, SinceID: after, Limit: pageSize})
				if err != nil {
					return err
				}
				for _, e := range page {
					if !e.TS.Before(since) {
						in.Audit = append(in.Audit, e)
					}
				}
				if len(page) < pageSize {
					break
				}
				after = page[len(page)-1].ID
			}
		}
		for _, a := range in.Agents {
			var after int64
			for {
				page, err := tx.ListMessages(model.ThreadAgent, a.ID, after, pageSize)
				if err != nil {
					return err
				}
				for _, m := range page {
					if m.Kind == model.MsgSystem && !m.CreatedAt.Before(since) {
						in.Messages = append(in.Messages, m)
					}
				}
				if len(page) < pageSize {
					break
				}
				after = page[len(page)-1].ID
			}
		}
		return nil
	})
	if err != nil {
		return Input{}, err
	}
	in.Errors, err = s.Errors.ListToolErrors(ctx, since, errorLimit)
	return in, err
}
