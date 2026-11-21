package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const (
	DefaultConnectTimeout = 5 * time.Minute
	DefaultIdleTTL        = 15 * time.Minute
	DefaultInterval       = 30 * time.Second

	ReasonConnectTimeout = "connect_timeout"
	ReasonIdle           = "idle"
	ReasonBriefClosed    = "brief_closed"
	ReasonPaused         = "paused"
	ReasonAgentRevoked   = "agent_revoked"
	ReasonExited         = "exited"
	ReasonOwner          = "owner"

	SpawnRequested = "requested"
	SpawnSpawned   = "spawned"
	SpawnFailed    = "failed"
	SpawnStopped   = "stopped"
)

const busyStatuses = "assigned,in_progress,blocked,submitted,in_review,changes_requested,needs_owner"

var system = model.Actor{ID: "system", Role: "system"}

type Record struct {
	SpawnID          string
	ConnectedAt      time.Time
	IdleSince        time.Time
	StoppedAt        time.Time
	StopReason       string
	Process          string
	ProcessCheckedAt time.Time
}

type Store interface {
	Records(ctx context.Context) (map[string]Record, error)
	Save(ctx context.Context, rec Record, sp *model.Spawn, ev *model.AuditEvent) error
}

type Config struct {
	ConnectTimeout time.Duration
	IdleTTL        time.Duration
	PauseGrace     time.Duration
	Interval       time.Duration
}

type Options struct {
	Config
	Board   *board.Board
	Store   Store
	Spawner spawner.Spawner
	Hub     events.Publisher
	Now     func() time.Time
	Log     *slog.Logger
}

type Spawn struct {
	*model.Spawn
	Lifecycle        string     `json:"lifecycle"`
	ConnectedAt      *time.Time `json:"connected_at"`
	IdleSince        *time.Time `json:"idle_since"`
	StoppedAt        *time.Time `json:"stopped_at"`
	StopReason       *string    `json:"stop_reason"`
	Process          string     `json:"process"`
	ProcessCheckedAt *time.Time `json:"process_checked_at"`
}

type Reaper struct {
	o        Options
	mu       sync.Mutex
	pausedAt time.Time
}

func New(o Options) *Reaper {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Reaper{o: o}
}

func (r *Reaper) Run(ctx context.Context) {
	if err := r.Reconcile(ctx); err != nil && ctx.Err() == nil {
		r.o.Log.Error("spawn reconcile", "err", err)
	}
	t := time.NewTicker(r.o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
				r.o.Log.Error("spawn reaper", "err", err)
			}
		}
	}
}

type snapshot struct {
	spawns []*model.Spawn
	recs   map[string]Record
	agents map[string]*model.Agent
}

func (r *Reaper) load(ctx context.Context, briefID string) (*snapshot, error) {
	sps, err := r.o.Board.ListSpawns(ctx, briefID)
	if err != nil {
		return nil, err
	}
	recs, err := r.o.Store.Records(ctx)
	if err != nil {
		return nil, err
	}
	as, err := r.o.Board.ListAgents(ctx, "", "")
	if err != nil {
		return nil, err
	}
	s := &snapshot{spawns: sps, recs: recs, agents: make(map[string]*model.Agent, len(as))}
	for _, a := range as {
		s.agents[a.ID] = a
	}
	return s, nil
}

func (s *snapshot) record(id string) Record {
	rec := s.recs[id]
	rec.SpawnID = id
	return rec
}

func (r *Reaper) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.load(ctx, "")
	if err != nil {
		return err
	}
	ts, err := r.o.Board.ListTasks(ctx, board.TaskQuery{Status: busyStatuses, Limit: 100000})
	if err != nil {
		return err
	}
	busy := map[string]bool{}
	for _, t := range ts {
		for _, id := range []*string{t.Assignee, t.Reviewer} {
			if id != nil {
				busy[*id] = true
			}
		}
	}
	closed, err := r.o.Board.ListBriefs(ctx, "closed")
	if err != nil {
		return err
	}
	closedBrief := map[string]bool{}
	for _, b := range closed {
		closedBrief[b.ID] = true
	}
	now := r.o.Now()
	paused, _ := r.o.Board.Paused()
	switch {
	case !paused:
		r.pausedAt = time.Time{}
	case r.pausedAt.IsZero():
		r.pausedAt = now
	}
	pauseStop := paused && r.o.PauseGrace > 0 && now.Sub(r.pausedAt) >= r.o.PauseGrace

	var errs []error
	for _, sp := range s.spawns {
		if sp.Status != SpawnSpawned {
			continue
		}
		rec := s.record(sp.ID)
		a := s.agents[sp.AgentID]
		var err error
		switch {
		case a == nil || a.Status == model.AgentRevoked:
			reason := rec.StopReason
			if reason == "" {
				reason = ReasonAgentRevoked
			}
			_, err = r.stop(ctx, sp, rec, a, system, reason)
		case closedBrief[sp.BriefID]:
			_, err = r.stop(ctx, sp, rec, a, system, ReasonBriefClosed)
		case pauseStop:
			_, err = r.stop(ctx, sp, rec, a, system, ReasonPaused)
		case a.LastSeenAt == nil:
			if r.o.ConnectTimeout > 0 && now.Sub(sp.CreatedAt) >= r.o.ConnectTimeout {
				_, err = r.stop(ctx, sp, rec, a, system, ReasonConnectTimeout)
			}
		default:
			err = r.observe(ctx, sp, rec, a, busy[a.ID], now)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Reaper) observe(ctx context.Context, sp *model.Spawn, rec Record, a *model.Agent, busy bool, now time.Time) error {
	before := rec
	if rec.ConnectedAt.IsZero() {
		rec.ConnectedAt = *a.LastSeenAt
	}
	switch {
	case busy:
		rec.IdleSince = time.Time{}
	case rec.IdleSince.IsZero():
		rec.IdleSince = now
	}
	if !busy && r.o.IdleTTL > 0 && now.Sub(rec.IdleSince) >= r.o.IdleTTL {
		_, err := r.stop(ctx, sp, rec, a, system, ReasonIdle)
		return err
	}
	if rec == before {
		return nil
	}
	return r.o.Store.Save(ctx, rec, nil, nil)
}

func (r *Reaper) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.load(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, sp := range s.spawns {
		a := s.agents[sp.AgentID]
		if sp.Status != SpawnSpawned || a == nil || a.Status == model.AgentRevoked {
			continue
		}
		rec := s.record(sp.ID)
		st, err := r.o.Spawner.Status(ctx, sp.AgentID)
		if err != nil {
			if !errors.Is(err, spawner.ErrNotImplemented) {
				r.o.Log.Warn("spawn status", "spawn", sp.ID, "agent", sp.AgentID, "err", err)
			}
			st = spawner.StatusUnknown
		}
		if st == spawner.StatusExited {
			_, err = r.stop(ctx, sp, rec, a, system, ReasonExited)
		} else {
			rec.Process, rec.ProcessCheckedAt = string(st), r.o.Now()
			err = r.o.Store.Save(ctx, rec, nil, nil)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Reaper) Stop(ctx context.Context, spawnID string) (*Spawn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.load(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, sp := range s.spawns {
		if sp.ID != spawnID {
			continue
		}
		if sp.Status != SpawnSpawned {
			return nil, model.Conflict("spawn %s is %s", sp.ID, sp.Status)
		}
		a := s.agents[sp.AgentID]
		rec, err := r.stop(ctx, sp, s.record(sp.ID), a, model.Owner, ReasonOwner)
		if err != nil {
			r.o.Log.Error("spawn stop", "spawn", sp.ID, "err", err)
			return nil, model.Errf("internal", "spawn %s: stop failed", sp.ID)
		}
		v := view(sp, rec, a)
		return &v, nil
	}
	return nil, model.NotFound("spawn %s not found", spawnID)
}

func (r *Reaper) stop(ctx context.Context, sp *model.Spawn, rec Record, a *model.Agent, actor model.Actor, reason string) (Record, error) {
	if a != nil && a.Status != model.AgentRevoked {
		if _, err := r.o.Board.RevokeAgent(ctx, sp.AgentID, "spawn stopped: "+reason); err != nil {
			return rec, fmt.Errorf("revoke %s: %w", sp.AgentID, err)
		}
	}
	if rec.StopReason == "" {
		rec.StopReason = reason
	}
	stopErr := r.o.Spawner.Stop(ctx, sp.AgentID)
	if stopErr != nil && !errors.Is(stopErr, spawner.ErrNotImplemented) {
		if err := r.o.Store.Save(ctx, rec, nil, nil); err != nil {
			return rec, err
		}
		return rec, fmt.Errorf("stop %s: %w", sp.ID, stopErr)
	}
	now := r.o.Now()
	if stopErr == nil {
		rec.Process, rec.ProcessCheckedAt = string(spawner.StatusExited), now
	}
	rec.StoppedAt, rec.IdleSince = now, time.Time{}
	cp := *sp
	cp.Status = SpawnStopped
	if rec.StopReason == ReasonConnectTimeout {
		cp.Status = SpawnFailed
		cp.Error = ptr(fmt.Sprintf("no heartbeat or tool call within %s", r.o.ConnectTimeout))
	}
	ev := &model.AuditEvent{TS: now, AgentID: actor.ID, Role: actor.Role, Tool: "stop_spawn",
		Before: map[string]any{"spawn_id": sp.ID, "status": sp.Status},
		After:  map[string]any{"spawn_id": sp.ID, "status": cp.Status, "reason": rec.StopReason}}
	if err := r.o.Store.Save(ctx, rec, &cp, ev); err != nil {
		return rec, err
	}
	*sp = cp
	if a != nil {
		a.Status = model.AgentRevoked
	}
	if r.o.Hub != nil {
		r.o.Hub.Publish([]string{"agents", "brief:" + cp.BriefID}, "spawn.updated", &cp)
	}
	return rec, nil
}

func (r *Reaper) List(ctx context.Context, briefID string) ([]Spawn, error) {
	s, err := r.load(ctx, briefID)
	if err != nil {
		return nil, err
	}
	out := make([]Spawn, len(s.spawns))
	for i, sp := range s.spawns {
		out[i] = view(sp, s.record(sp.ID), s.agents[sp.AgentID])
	}
	return out, nil
}

func view(sp *model.Spawn, rec Record, a *model.Agent) Spawn {
	if rec.ConnectedAt.IsZero() && a != nil && a.LastSeenAt != nil && sp.Status == SpawnSpawned {
		rec.ConnectedAt = *a.LastSeenAt
	}
	v := Spawn{Spawn: sp, ConnectedAt: tptr(rec.ConnectedAt), IdleSince: tptr(rec.IdleSince), StoppedAt: tptr(rec.StoppedAt),
		Process: rec.Process, ProcessCheckedAt: tptr(rec.ProcessCheckedAt)}
	if rec.StopReason != "" {
		v.StopReason = ptr(rec.StopReason)
	}
	if v.Process == "" {
		v.Process = string(spawner.StatusUnknown)
	}
	switch {
	case sp.Status == SpawnRequested:
		v.Lifecycle = "pending"
	case sp.Status != SpawnSpawned:
		v.Lifecycle = sp.Status
	case rec.ConnectedAt.IsZero():
		v.Lifecycle = "connecting"
	case !rec.IdleSince.IsZero():
		v.Lifecycle = "idle"
	default:
		v.Lifecycle = "active"
	}
	return v
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func ptr[T any](v T) *T { return &v }
