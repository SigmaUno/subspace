package autopilot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/model"
)

type Board interface {
	Paused() (bool, string)
	ListBriefs(ctx context.Context, status string) ([]*model.Brief, error)
	GetBrief(ctx context.Context, id string) (*model.Brief, error)
	ListTasks(ctx context.Context, q board.TaskQuery) ([]*model.Task, error)
	ListAgents(ctx context.Context, role, status string) ([]*model.Agent, error)
	Capacity(ctx context.Context) (*board.CapacityView, error)
	Assign(ctx context.Context, a model.Actor, taskID, agentID, as string) (*model.Task, error)
	RequestSpawn(ctx context.Context, a model.Actor, in board.SpawnInput) ([]*model.Spawn, error)
}

type Options struct {
	Actor        model.Actor
	DefaultSpawn SpawnConfig
	Spawn        map[string]SpawnConfig
	History      int
	Scores       func(ctx context.Context) (map[string]float64, error)
	Now          func() time.Time
	Log          *slog.Logger
}

type Result struct {
	Action
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
	Code  string    `json:"code,omitempty"`
}

type Status struct {
	State       string     `json:"state"`
	LastTick    *time.Time `json:"last_tick"`
	LastError   string     `json:"last_error,omitempty"`
	LastActions []Result   `json:"last_actions"`
}

type Service struct {
	b Board
	o Options

	mu       sync.Mutex
	enabled  map[string]bool
	running  bool
	lastTick *time.Time
	lastErr  string
	results  []Result
}

var expected = map[string]bool{"capacity": true, "paused": true, "budget_exceeded": true, "conflict": true}

func New(b Board, o Options) *Service {
	if o.Actor.ID == "" {
		o.Actor = model.Actor{ID: "autopilot", Role: model.RoleLeader}
	}
	if o.History <= 0 {
		o.History = 100
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{b: b, o: o, enabled: map[string]bool{}}
}

func (s *Service) SetEnabled(briefID string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.enabled[briefID] = true
	} else {
		delete(s.enabled, briefID)
	}
}

func (s *Service) Enabled(briefID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled[briefID]
}

func (s *Service) enabledCopy() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.enabled))
	for k, v := range s.enabled {
		out[k] = v
	}
	return out
}

func (s *Service) spawnConfig(briefID string) SpawnConfig {
	if c, ok := s.o.Spawn[briefID]; ok {
		return c
	}
	return s.o.DefaultSpawn
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	snap := Snapshot{Enabled: s.enabledCopy(), Spawn: map[string]SpawnConfig{}, Conflict: conflict.Warn}
	if c, ok := s.b.(interface{ ConflictMode() conflict.Mode }); ok {
		snap.Conflict = c.ConflictMode()
	}
	if snap.Paused, _ = s.b.Paused(); snap.Paused || len(snap.Enabled) == 0 {
		return snap, nil
	}
	var err error
	if snap.Briefs, err = s.b.ListBriefs(ctx, "open"); err != nil {
		return snap, err
	}
	for _, b := range snap.Briefs {
		snap.Spawn[b.ID] = s.spawnConfig(b.ID)
	}
	statuses := model.StatusReady + "," + model.StatusAssigned + "," + model.StatusInProgress + "," +
		model.StatusSubmitted + "," + model.StatusInReview
	if snap.Tasks, err = s.b.ListTasks(ctx, board.TaskQuery{Status: statuses, Limit: 100000}); err != nil {
		return snap, err
	}
	if snap.Agents, err = s.b.ListAgents(ctx, "", ""); err != nil {
		return snap, err
	}
	c, err := s.b.Capacity(ctx)
	if err != nil {
		return snap, err
	}
	snap.Capacity = *c
	if s.o.Scores != nil {
		if snap.Scores, err = s.o.Scores(ctx); err != nil {
			s.o.Log.Warn("autopilot agent scores unavailable", "err", err)
		}
	}
	return snap, nil
}

func (s *Service) Tick(ctx context.Context) ([]Result, error) {
	snap, err := s.Snapshot(ctx)
	now := s.o.Now()
	if err != nil {
		s.record(now, nil, err)
		return nil, err
	}
	var out []Result
	for _, a := range Plan(snap) {
		if p, _ := s.b.Paused(); p {
			break
		}
		r := Result{Action: a, At: s.o.Now()}
		if err := s.apply(ctx, a); err != nil {
			e := model.AsError(err)
			r.Error, r.Code = err.Error(), e.Code
			if !expected[e.Code] {
				s.o.Log.Warn("autopilot action failed", "kind", a.Kind, "task", a.TaskID, "agent", a.AgentID, "err", err)
			}
		}
		out = append(out, r)
	}
	s.record(now, out, nil)
	return out, nil
}

func (s *Service) apply(ctx context.Context, a Action) error {
	switch a.Kind {
	case ActAssign:
		_, err := s.b.Assign(ctx, s.o.Actor, a.TaskID, a.AgentID, "assignee")
		return err
	case ActReview:
		_, err := s.b.Assign(ctx, s.o.Actor, a.TaskID, a.AgentID, "reviewer")
		return err
	case ActSpawn:
		_, err := s.b.RequestSpawn(ctx, s.o.Actor, *a.Spawn)
		return err
	}
	return model.BadRequest("unknown action %q", a.Kind)
}

func (s *Service) record(at time.Time, rs []Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTick = &at
	s.lastErr = ""
	if err != nil {
		s.lastErr = err.Error()
	}
	s.results = append(s.results, rs...)
	if n := len(s.results) - s.o.History; n > 0 {
		s.results = append([]Result(nil), s.results[n:]...)
	}
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	s.setRunning(true)
	defer s.setRunning(false)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.o.Log.Error("autopilot tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) setRunning(on bool) {
	s.mu.Lock()
	s.running = on
	s.mu.Unlock()
}

func (s *Service) Status() Status {
	paused, _ := s.b.Paused()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{State: "stopped", LastTick: s.lastTick, LastError: s.lastErr, LastActions: append([]Result{}, s.results...)}
	switch {
	case !s.running:
	case paused:
		st.State = "paused"
	default:
		st.State = "running"
	}
	return st
}
