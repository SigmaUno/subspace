package components

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const DefaultStaleAfter = 5 * time.Minute

type Owner struct {
	AgentID     string   `json:"agent_id"`
	Role        string   `json:"role"`
	AgentStatus string   `json:"agent_status"`
	TaskIDs     []string `json:"task_ids"`
}

type Rollup struct {
	Counts       map[string]int `json:"counts"`
	Total        int            `json:"total"`
	ProgressAvg  float64        `json:"progress_avg"`
	Blocked      int            `json:"blocked"`
	Stale        int            `json:"stale"`
	LastActivity *time.Time     `json:"last_activity"`
}

type Node struct {
	Component
	Owners []Owner `json:"owners"`
	Status Rollup  `json:"status"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Graph struct {
	BriefID string `json:"brief_id"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

type Service struct {
	store      Store
	src        Source
	now        func() time.Time
	StaleAfter time.Duration
}

func NewService(store Store, src Source, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, src: src, now: now, StaleAfter: DefaultStaleAfter}
}

func (s *Service) SetPlan(ctx context.Context, briefID string, specs []Spec) ([]Component, error) {
	if _, err := s.src.Brief(ctx, briefID); err != nil {
		return nil, err
	}
	deps, err := resolve(specs)
	if err != nil {
		return nil, err
	}
	comps := make([]Component, len(specs))
	for i, sp := range specs {
		comps[i] = Component{
			ID:          fmt.Sprintf("C-%d", i+1),
			BriefID:     briefID,
			Name:        strings.TrimSpace(sp.Name),
			Description: sp.Description,
			Paths:       []string{},
			DependsOn:   []string{},
		}
		for _, p := range sp.Paths {
			c, _ := cleanPath(p)
			comps[i].Paths = append(comps[i].Paths, c)
		}
		for _, d := range deps[i] {
			comps[i].DependsOn = append(comps[i].DependsOn, fmt.Sprintf("C-%d", d+1))
		}
	}
	if err := s.store.Replace(ctx, briefID, comps); err != nil {
		return nil, err
	}
	return comps, nil
}

func (s *Service) Paths(ctx context.Context, briefID, componentID string) []string {
	comps, err := s.store.List(ctx, briefID)
	if err != nil {
		return nil
	}
	for _, c := range comps {
		if c.ID == componentID {
			return c.Paths
		}
	}
	return nil
}

func (s *Service) Graph(ctx context.Context, briefID string) (*Graph, error) {
	if _, err := s.src.Brief(ctx, briefID); err != nil {
		return nil, err
	}
	comps, err := s.store.List(ctx, briefID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.src.Tasks(ctx, briefID)
	if err != nil {
		return nil, err
	}
	agents, err := s.src.Agents(ctx)
	if err != nil {
		return nil, err
	}
	agentByID := make(map[string]*model.Agent, len(agents))
	for _, a := range agents {
		agentByID[a.ID] = a
	}
	tasksBy := make(map[string][]Task, len(comps))
	for _, t := range tasks {
		tasksBy[t.ComponentID] = append(tasksBy[t.ComponentID], t)
	}

	now := s.now()
	g := &Graph{BriefID: briefID, Nodes: make([]Node, 0, len(comps)), Edges: []Edge{}}
	for _, c := range comps {
		g.Nodes = append(g.Nodes, Node{
			Component: c,
			Owners:    owners(tasksBy[c.ID], agentByID),
			Status:    s.rollup(tasksBy[c.ID], now),
		})
		for _, d := range c.DependsOn {
			g.Edges = append(g.Edges, Edge{From: c.ID, To: d})
		}
	}
	return g, nil
}

func owners(tasks []Task, agents map[string]*model.Agent) []Owner {
	byAgent := make(map[string]*Owner)
	add := func(id, fallbackRole, taskID string) {
		o, ok := byAgent[id]
		if !ok {
			o = &Owner{AgentID: id, Role: fallbackRole, AgentStatus: "unknown", TaskIDs: []string{}}
			if a := agents[id]; a != nil {
				o.Role, o.AgentStatus = a.Role, a.Status
			}
			byAgent[id] = o
		}
		o.TaskIDs = append(o.TaskIDs, taskID)
	}
	for _, t := range tasks {
		if t.Status == model.StatusCancelled {
			continue
		}
		if t.Assignee != nil {
			add(*t.Assignee, t.Role, t.ID)
		}
		if t.Reviewer != nil {
			add(*t.Reviewer, model.RoleReviewer, t.ID)
		}
	}
	out := make([]Owner, 0, len(byAgent))
	for _, o := range byAgent {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

func (s *Service) rollup(tasks []Task, now time.Time) Rollup {
	r := Rollup{Counts: make(map[string]int, len(model.AllStatuses))}
	for _, st := range model.AllStatuses {
		r.Counts[st] = 0
	}
	var progress, counted int
	for _, t := range tasks {
		r.Counts[t.Status]++
		r.Total++
		if t.Status != model.StatusCancelled {
			counted++
			if t.Status == model.StatusDone {
				progress += 100
			} else {
				progress += t.ProgressPct
			}
		}
		if t.Status == model.StatusBlocked || t.Status == model.StatusNeedsOwner {
			r.Blocked++
		}
		if s.stale(t, now) {
			r.Stale++
		}
		r.LastActivity = later(r.LastActivity, t.UpdatedAt)
		if t.LastHeartbeatAt != nil {
			r.LastActivity = later(r.LastActivity, *t.LastHeartbeatAt)
		}
	}
	if counted > 0 {
		r.ProgressAvg = math.Round(float64(progress)/float64(counted)*10) / 10
	}
	return r
}

func (s *Service) stale(t Task, now time.Time) bool {
	if t.Status != model.StatusInProgress {
		return false
	}
	if t.LeaseExpiresAt != nil && t.LeaseExpiresAt.Before(now) {
		return true
	}
	seen := t.UpdatedAt
	switch {
	case t.LastHeartbeatAt != nil:
		seen = *t.LastHeartbeatAt
	case t.ClaimedAt != nil:
		seen = *t.ClaimedAt
	}
	return now.Sub(seen) > s.StaleAfter
}

func later(cur *time.Time, t time.Time) *time.Time {
	if t.IsZero() || (cur != nil && !t.After(*cur)) {
		return cur
	}
	return &t
}
