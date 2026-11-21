package quota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS agent_quotas (
  agent_id TEXT PRIMARY KEY, max_cost_eur_per_day DOUBLE PRECISION NOT NULL,
  max_tasks_per_day INTEGER NOT NULL, max_concurrent_tasks INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_quota_days (
  agent_id TEXT NOT NULL, day BIGINT NOT NULL, cost_eur DOUBLE PRECISION NOT NULL, warned INTEGER NOT NULL,
  PRIMARY KEY (agent_id, day)
);
CREATE TABLE IF NOT EXISTS agent_quota_tasks (
  agent_id TEXT NOT NULL, day BIGINT NOT NULL, task_id TEXT NOT NULL,
  PRIMARY KEY (agent_id, day, task_id)
);
`

const (
	retainDays = 7
	taskLimit  = 100000
)

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(schema, "agent_quotas", "agent_quota_days", "agent_quota_tasks")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("quota: store transaction does not expose SQL")

func sqlOf(tx store.Tx) (sqlTx, error) {
	q, ok := tx.(sqlTx)
	if !ok {
		return nil, errNoSQL
	}
	return q, nil
}

type Options struct {
	Store store.Store
	Pub   events.Publisher
	Now   func() time.Time
}

type Service struct {
	st  store.Store
	pub events.Publisher
	now func() time.Time

	mu      sync.Mutex
	gen     uint64
	setting int
	limits  map[string]Quota
	pruned  map[string]int64
}

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{st: o.Store, pub: o.Pub, now: o.Now, pruned: map[string]int64{}}
}

func (s *Service) limit(q sqlTx, agent string) (Quota, error) {
	s.mu.Lock()
	if s.setting > 0 {
		s.mu.Unlock()
		return loadQuota(q, agent)
	}
	if s.limits != nil {
		lim := s.limits[agent]
		s.mu.Unlock()
		return lim, nil
	}
	gen := s.gen
	s.mu.Unlock()
	all, err := loadQuotas(q)
	if err != nil {
		return Quota{}, err
	}
	s.mu.Lock()
	if s.gen == gen && s.setting == 0 {
		s.limits = all
	}
	s.mu.Unlock()
	return all[agent], nil
}

func (s *Service) invalidate(delta int) {
	s.mu.Lock()
	s.setting += delta
	s.gen++
	s.limits = nil
	s.mu.Unlock()
}

func (s *Service) prune(q sqlTx, agent string, day int64) error {
	s.mu.Lock()
	done := s.pruned[agent] == day
	s.mu.Unlock()
	if done {
		return nil
	}
	if err := prune(q, agent, day); err != nil {
		return err
	}
	s.mu.Lock()
	s.pruned[agent] = day
	s.mu.Unlock()
	return nil
}

func (s *Service) Gate(tx store.Tx, ev board.GateEvent) (*board.Notice, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	lim, err := s.limit(q, ev.AgentID)
	if err != nil {
		return nil, err
	}
	day := DayStart(ev.Now).Unix()
	switch ev.Op {
	case board.GateClaim, board.GateAssign:
		return nil, admit(tx, q, ev, lim, day)
	case board.GateClaimed:
		if _, err := q.Exec(`INSERT INTO agent_quota_tasks(agent_id,day,task_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, ev.AgentID, day, ev.Task.ID); err != nil {
			return nil, err
		}
		if err := s.prune(q, ev.AgentID, day); err != nil {
			return nil, err
		}
	case board.GateCost:
		if _, err := q.Exec(`INSERT INTO agent_quota_days(agent_id,day,cost_eur,warned) VALUES(?,?,?,0)
			ON CONFLICT(agent_id,day) DO UPDATE SET cost_eur=agent_quota_days.cost_eur+excluded.cost_eur`, ev.AgentID, day, ev.CostEUR); err != nil {
			return nil, err
		}
	}
	return warn(tx, q, ev.AgentID, lim, ev.Now)
}

func admit(tx store.Tx, q sqlTx, ev board.GateEvent, lim Quota, day int64) error {
	if lim.Unlimited() {
		return nil
	}
	u, l, err := usageOf(tx, q, ev.AgentID, day)
	if err != nil {
		return err
	}
	newTask := true
	if ev.Op == board.GateAssign {
		u.Tasks += l.queued
		u.ConcurrentTasks = l.held
	} else {
		var n int
		if err := scan(q, []any{&n}, `SELECT count(*) FROM agent_quota_tasks WHERE agent_id=? AND day=? AND task_id=?`, ev.AgentID, day, ev.Task.ID); err != nil {
			return err
		}
		newTask = n == 0
	}
	if limit, hit := Exceeded(lim, u, newTask); hit {
		return model.Errf("quota_exceeded", "agent %s reached its daily quota (%s); the window resets %s",
			ev.AgentID, limit, DayEnd(ev.Now).Format(time.RFC3339))
	}
	return nil
}

type load struct{ held, queued int }

func usageOf(tx store.Tx, q sqlTx, agent string, day int64) (Usage, load, error) {
	var u Usage
	var l load
	if err := scan(q, []any{&u.CostEUR}, `SELECT cost_eur FROM agent_quota_days WHERE agent_id=? AND day=?`, agent, day); err != nil {
		return u, l, err
	}
	if err := scan(q, []any{&u.Tasks}, `SELECT count(*) FROM agent_quota_tasks WHERE agent_id=? AND day=?`, agent, day); err != nil {
		return u, l, err
	}
	ts, err := tx.ListTasks(store.TaskFilter{Assignee: agent, Limit: taskLimit,
		Status: []string{model.StatusInProgress, model.StatusAssigned, model.StatusChangesRequested}})
	if err != nil {
		return u, l, err
	}
	for _, t := range ts {
		l.held++
		switch t.Status {
		case model.StatusInProgress:
			u.ConcurrentTasks++
		case model.StatusAssigned:
			l.queued++
		}
	}
	return u, l, nil
}

func scan(q sqlTx, dest []any, query string, args ...any) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
	}
	return rows.Err()
}

func each(q sqlTx, fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func loadQuota(q sqlTx, agent string) (Quota, error) {
	var lim Quota
	err := scan(q, []any{&lim.MaxCostEURPerDay, &lim.MaxTasksPerDay, &lim.MaxConcurrentTasks},
		`SELECT max_cost_eur_per_day,max_tasks_per_day,max_concurrent_tasks FROM agent_quotas WHERE agent_id=?`, agent)
	return lim, err
}

func loadQuotas(q sqlTx) (map[string]Quota, error) {
	out := map[string]Quota{}
	err := each(q, func(r *sql.Rows) error {
		var id string
		var lim Quota
		if err := r.Scan(&id, &lim.MaxCostEURPerDay, &lim.MaxTasksPerDay, &lim.MaxConcurrentTasks); err != nil {
			return err
		}
		out[id] = lim
		return nil
	}, `SELECT agent_id,max_cost_eur_per_day,max_tasks_per_day,max_concurrent_tasks FROM agent_quotas`)
	return out, err
}

func prune(q sqlTx, agent string, day int64) error {
	old := day - retainDays*int64(24*time.Hour/time.Second)
	for _, table := range []string{"agent_quota_days", "agent_quota_tasks"} {
		if _, err := q.Exec(`DELETE FROM `+table+` WHERE agent_id=? AND day<?`, agent, old); err != nil {
			return err
		}
	}
	return nil
}

func warn(tx store.Tx, q sqlTx, agent string, lim Quota, now time.Time) (*board.Notice, error) {
	if lim.Unlimited() {
		return nil, nil
	}
	day := DayStart(now).Unix()
	u, _, err := usageOf(tx, q, agent, day)
	if err != nil {
		return nil, err
	}
	level := LevelOf(lim, u)
	if level != LevelWarning && level != LevelExceeded {
		return nil, nil
	}
	if _, err := q.Exec(`INSERT INTO agent_quota_days(agent_id,day,cost_eur,warned) VALUES(?,?,0,0) ON CONFLICT(agent_id,day) DO NOTHING`, agent, day); err != nil {
		return nil, err
	}
	var warned int
	if err := scan(q, []any{&warned}, `SELECT warned FROM agent_quota_days WHERE agent_id=? AND day=?`, agent, day); err != nil {
		return nil, err
	}
	if warned != 0 {
		return nil, nil
	}
	if _, err := q.Exec(`UPDATE agent_quota_days SET warned=1 WHERE agent_id=? AND day=?`, agent, day); err != nil {
		return nil, err
	}
	return &board.Notice{Topics: []string{"control"}, Type: "quota.warning", Data: map[string]any{
		"agent_id": agent, "percent": Percent(lim, u), "level": string(level), "cost_eur": u.CostEUR, "tasks": u.Tasks,
		"concurrent_tasks": u.ConcurrentTasks, "starts_at": DayStart(now),
	}}, nil
}

func (s *Service) Set(ctx context.Context, id string, lim Quota) error {
	if err := lim.Validate(); err != nil {
		return err
	}
	var notice *board.Notice
	s.invalidate(1)
	defer s.invalidate(-1)
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		ag, err := tx.GetAgent(id)
		if errors.Is(err, store.ErrNotFound) {
			return model.NotFound("agent %s not found", id)
		}
		if err != nil {
			return err
		}
		if ag.Status == model.AgentRevoked {
			return model.Conflict("agent %s is revoked", id)
		}
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		before, err := loadQuota(q, id)
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := q.Exec(`INSERT INTO agent_quotas(agent_id,max_cost_eur_per_day,max_tasks_per_day,max_concurrent_tasks) VALUES(?,?,?,?)
			ON CONFLICT(agent_id) DO UPDATE SET max_cost_eur_per_day=excluded.max_cost_eur_per_day,
			max_tasks_per_day=excluded.max_tasks_per_day, max_concurrent_tasks=excluded.max_concurrent_tasks`,
			id, lim.MaxCostEURPerDay, lim.MaxTasksPerDay, lim.MaxConcurrentTasks); err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE agent_quota_days SET warned=0 WHERE agent_id=? AND day=?`, id, DayStart(now).Unix()); err != nil {
			return err
		}
		note := "agent " + id
		if err := tx.InsertAudit(&model.AuditEvent{
			TS: now, AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: "set_agent_quota", Note: &note,
			Before: map[string]any{"agent_id": id, "quota": before}, After: map[string]any{"agent_id": id, "quota": lim},
		}); err != nil {
			return err
		}
		notice, err = warn(tx, q, id, lim, now)
		return err
	})
	if err != nil {
		return err
	}
	if notice != nil && s.pub != nil {
		s.pub.Publish(notice.Topics, notice.Type, notice.Data)
	}
	return nil
}

type Snapshot struct {
	now    time.Time
	quotas map[string]Quota
	usage  map[string]*Usage
}

func (sn *Snapshot) Of(id string) (Quota, Today) {
	var u Usage
	if v := sn.usage[id]; v != nil {
		u = *v
	}
	lim := sn.quotas[id]
	return lim, Window(lim, u, sn.now)
}

func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	now := s.now()
	day := DayStart(now).Unix()
	sn := &Snapshot{now: now, usage: map[string]*Usage{}}
	at := func(id string) *Usage {
		if sn.usage[id] == nil {
			sn.usage[id] = &Usage{}
		}
		return sn.usage[id]
	}
	err := s.st.View(ctx, func(tx store.Tx) error {
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		if sn.quotas, err = loadQuotas(q); err != nil {
			return err
		}
		if err := each(q, func(r *sql.Rows) error {
			var id string
			var cost float64
			if err := r.Scan(&id, &cost); err != nil {
				return err
			}
			at(id).CostEUR = cost
			return nil
		}, `SELECT agent_id,cost_eur FROM agent_quota_days WHERE day=?`, day); err != nil {
			return err
		}
		if err := each(q, func(r *sql.Rows) error {
			var id string
			var n int
			if err := r.Scan(&id, &n); err != nil {
				return err
			}
			at(id).Tasks = n
			return nil
		}, `SELECT agent_id,count(*) FROM agent_quota_tasks WHERE day=? GROUP BY agent_id`, day); err != nil {
			return err
		}
		ts, err := tx.ListTasks(store.TaskFilter{Status: []string{model.StatusInProgress}, Limit: taskLimit})
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.Assignee != nil {
				at(*t.Assignee).ConcurrentTasks++
			}
		}
		return nil
	})
	return sn, err
}

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	sn, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var active map[string]bool
	err = s.st.View(ctx, func(tx store.Tx) error {
		as, err := tx.ListAgents(store.AgentFilter{Status: model.AgentActive, Limit: taskLimit})
		active = make(map[string]bool, len(as))
		for _, a := range as {
			active[a.ID] = true
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []*decisions.Decision
	for id := range sn.quotas {
		lim, today := sn.Of(id)
		if !active[id] || (today.Level != LevelWarning && today.Level != LevelExceeded) {
			continue
		}
		out = append(out, &decisions.Decision{
			ID: "quota:" + id, Kind: decisions.KindBudget, AgentID: id,
			Title: fmt.Sprintf("Agent %s is at %.0f%% of its daily quota", id, today.Percent),
			Summary: fmt.Sprintf("€%.2f of %s spent, %d of %s tasks started, %d of %s running; the window resets %s",
				today.CostEUR, limitOf(lim.MaxCostEURPerDay), today.Tasks, limitOf(float64(lim.MaxTasksPerDay)),
				today.ConcurrentTasks, limitOf(float64(lim.MaxConcurrentTasks)), today.ResetsAt.Format(time.RFC3339)),
			Links:     decisions.Links{Thread: model.ThreadAgent + ":" + id},
			CreatedAt: today.DayStartsAt,
		})
	}
	return out, nil
}

func limitOf(v float64) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%g", v)
}
