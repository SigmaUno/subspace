package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type leaderTx struct {
	store.Tx
	leader string
	seen   map[string]bool
}

func scope(tx store.Tx, a model.Actor) store.Tx {
	if a.Role != model.RoleLeader {
		return tx
	}
	return &leaderTx{Tx: tx, leader: a.ID, seen: map[string]bool{}}
}

func unscoped(tx store.Tx) store.Tx {
	if s, ok := tx.(*leaderTx); ok {
		return s.Tx
	}
	return tx
}

func (b *Board) readAs(ctx context.Context, a model.Actor, fn func(tx store.Tx) error) error {
	return b.read(ctx, func(tx store.Tx) error { return fn(scope(tx, a)) })
}

func (b *Board) writeAs(ctx context.Context, a model.Actor, fn func(tx store.Tx, w *wctx) error) error {
	return b.write(ctx, func(tx store.Tx, w *wctx) error { return fn(scope(tx, a), w) })
}

func (t *leaderTx) owns(briefID string) (bool, error) {
	if v, ok := t.seen[briefID]; ok {
		return v, nil
	}
	br, err := t.Tx.GetBrief(briefID)
	if isErr(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t.seen[briefID] = br.LedBy(t.leader)
	return t.seen[briefID], nil
}

func (t *leaderTx) guard(briefID string) error {
	ok, err := t.owns(briefID)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

func filter[T any](t *leaderTx, brief func(T) string) func([]T, error) ([]T, error) {
	return func(in []T, err error) ([]T, error) {
		if err != nil {
			return nil, err
		}
		out := in[:0]
		for _, x := range in {
			ok, err := t.owns(brief(x))
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, x)
			}
		}
		return out, nil
	}
}

func owned[T any](t *leaderTx, brief func(T) string) func(T, error) (T, error) {
	return func(x T, err error) (T, error) {
		if err == nil {
			err = t.guard(brief(x))
		}
		if err != nil {
			var zero T
			return zero, err
		}
		return x, nil
	}
}

func (t *leaderTx) guarded(briefID string, op func() error) error {
	if err := t.guard(briefID); err != nil {
		return err
	}
	return op()
}

func planBrief(p *model.Plan) string   { return p.BriefID }
func taskBrief(x *model.Task) string   { return x.BriefID }
func spawnBrief(s *model.Spawn) string { return s.BriefID }

func (t *leaderTx) GetBrief(id string) (*model.Brief, error) {
	br, err := t.Tx.GetBrief(id)
	if err != nil {
		return nil, err
	}
	t.seen[id] = br.LedBy(t.leader)
	if !t.seen[id] {
		return nil, store.ErrNotFound
	}
	return br, nil
}

func (t *leaderTx) UpdateBrief(br *model.Brief) error {
	return t.guarded(br.ID, func() error { return t.Tx.UpdateBrief(br) })
}

func (t *leaderTx) ListBriefs(f store.BriefFilter) ([]*model.Brief, error) {
	bs, err := t.Tx.ListBriefs(f)
	if err != nil {
		return nil, err
	}
	for _, br := range bs {
		t.seen[br.ID] = br.LedBy(t.leader)
	}
	return filter(t, func(br *model.Brief) string { return br.ID })(bs, nil)
}

func (t *leaderTx) InsertPlan(p *model.Plan) error {
	return t.guarded(p.BriefID, func() error { return t.Tx.InsertPlan(p) })
}

func (t *leaderTx) GetPlan(id string) (*model.Plan, error) {
	return owned(t, planBrief)(t.Tx.GetPlan(id))
}

func (t *leaderTx) UpdatePlan(p *model.Plan) error {
	return t.guarded(p.BriefID, func() error { return t.Tx.UpdatePlan(p) })
}

func (t *leaderTx) ListPlans(f store.PlanFilter) ([]*model.Plan, error) {
	return filter(t, planBrief)(t.Tx.ListPlans(f))
}

func (t *leaderTx) InsertTask(task *model.Task) error {
	return t.guarded(task.BriefID, func() error { return t.Tx.InsertTask(task) })
}

func (t *leaderTx) GetTask(id string) (*model.Task, error) {
	return owned(t, taskBrief)(t.Tx.GetTask(id))
}

func (t *leaderTx) UpdateTask(task *model.Task) error {
	return t.guarded(task.BriefID, func() error { return t.Tx.UpdateTask(task) })
}

func (t *leaderTx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	return filter(t, taskBrief)(t.Tx.ListTasks(f))
}

func (t *leaderTx) ListMessages(kind, id string, sinceID int64, limit int) ([]*model.Message, error) {
	switch kind {
	case model.ThreadTask:
		if _, err := t.GetTask(id); err != nil {
			return nil, err
		}
	case model.ThreadBrief:
		if err := t.guard(id); err != nil {
			return nil, err
		}
	}
	return t.Tx.ListMessages(kind, id, sinceID, limit)
}

func (t *leaderTx) GetQuestion(id string) (*model.Question, error) {
	q, err := t.Tx.GetQuestion(id)
	if err != nil {
		return nil, err
	}
	if q.BriefID != nil {
		if err := t.guard(*q.BriefID); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func (t *leaderTx) ListQuestions(f store.QuestionFilter) ([]*model.Question, error) {
	qs, err := t.Tx.ListQuestions(f)
	if err != nil {
		return nil, err
	}
	out := qs[:0]
	for _, q := range qs {
		if q.BriefID != nil {
			if ok, err := t.owns(*q.BriefID); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
		}
		out = append(out, q)
	}
	return out, nil
}

func (t *leaderTx) InsertSpawn(s *model.Spawn) error {
	return t.guarded(s.BriefID, func() error { return t.Tx.InsertSpawn(s) })
}

func (t *leaderTx) ListSpawns(f store.SpawnFilter) ([]*model.Spawn, error) {
	return filter(t, spawnBrief)(t.Tx.ListSpawns(f))
}

func (b *Board) BriefFor(ctx context.Context, a model.Actor, id string) (*model.Brief, error) {
	var out *model.Brief
	err := b.readAs(ctx, a, func(tx store.Tx) (e error) {
		if out, e = tx.GetBrief(id); e != nil {
			return
		}
		out.CostUsed, e = tx.BriefCost(id)
		return
	})
	return out, err
}

func (b *Board) BriefsFor(ctx context.Context, a model.Actor, status string) ([]*model.Brief, error) {
	var out []*model.Brief
	err := b.readAs(ctx, a, func(tx store.Tx) error {
		bs, err := tx.ListBriefs(store.BriefFilter{Status: status})
		if err != nil {
			return err
		}
		for _, br := range bs {
			if br.CostUsed, err = tx.BriefCost(br.ID); err != nil {
				return err
			}
		}
		out = bs
		return nil
	})
	return nz(out), err
}

func (b *Board) BoardFor(ctx context.Context, a model.Actor, briefID string) (*BoardView, error) {
	v := &BoardView{Columns: map[string][]*model.Task{}}
	for _, s := range model.AllStatuses {
		v.Columns[s] = []*model.Task{}
	}
	err := b.readAs(ctx, a, func(tx store.Tx) error {
		if briefID != "" {
			br, err := tx.GetBrief(briefID)
			if err != nil {
				return err
			}
			if br.CostUsed, err = tx.BriefCost(briefID); err != nil {
				return err
			}
			v.Brief = br
		}
		ts, err := tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: 100000})
		if err != nil {
			return err
		}
		for _, t := range ts {
			v.Columns[t.Status] = append(v.Columns[t.Status], b.overlay(t))
		}
		return nil
	})
	return v, err
}

func (b *Board) BriefVisible(ctx context.Context, a model.Actor, briefID string) (bool, error) {
	_, err := b.BriefFor(ctx, a, briefID)
	if err == nil {
		return true, nil
	}
	if model.AsError(err).Code == "not_found" {
		return false, nil
	}
	return false, err
}

func checkLeader(tx store.Tx, id string) (*string, error) {
	if id == "" {
		return nil, nil
	}
	ag, err := tx.GetAgent(id)
	if err != nil {
		return nil, model.NotFound("agent %s not found", id)
	}
	if ag.Role != model.RoleLeader {
		return nil, model.BadRequest("agent %s is a %s, not a leader", id, ag.Role)
	}
	if ag.Status == model.AgentRevoked {
		return nil, model.Conflict("agent %s is revoked", id)
	}
	return &id, nil
}

func (b *Board) handoff(tx store.Tx, w *wctx, br *model.Brief, to string) error {
	next, err := checkLeader(tx, to)
	if err != nil {
		return err
	}
	prev := deref(br.LeaderID)
	if prev == to {
		return nil
	}
	br.LeaderID = next
	if err := tx.UpdateBrief(br); err != nil {
		return err
	}
	label := func(id string) string {
		if id == "" {
			return "any leader"
		}
		return id
	}
	msg := fmt.Sprintf("Leader of %s changed: %s → %s", br.ID, label(prev), label(to))
	if err := w.post(tx, model.ThreadBrief, br.ID, model.Owner, model.MsgSystem, msg); err != nil {
		return err
	}
	if prev != "" {
		if err := w.post(tx, model.ThreadAgent, prev, model.Owner, model.MsgSystem,
			fmt.Sprintf("You are no longer the leader of brief %s.", br.ID)); err != nil {
			return err
		}
	}
	if to != "" {
		if err := w.post(tx, model.ThreadAgent, to, model.Owner, model.MsgSystem,
			fmt.Sprintf("You are now the leader of brief %s: %s", br.ID, br.Title)); err != nil {
			return err
		}
	}
	return w.audit(tx, model.Owner, "set_brief_leader", "", map[string]any{"brief_id": br.ID, "leader_id": prev},
		map[string]any{"brief_id": br.ID, "leader_id": to}, "")
}

func (b *Board) SetBriefLeader(ctx context.Context, id, leaderID string) (*model.Brief, error) {
	var out *model.Brief
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		br, err := tx.GetBrief(id)
		if err != nil {
			return err
		}
		if err := b.handoff(tx, w, br, leaderID); err != nil {
			return err
		}
		if br.CostUsed, err = tx.BriefCost(id); err != nil {
			return err
		}
		out = br
		return nil
	})
	return out, err
}

type sqlExec interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQLTx = errors.New("store transaction does not expose SQL")

func (t *leaderTx) Exec(q string, args ...any) (sql.Result, error) {
	if e, ok := t.Tx.(sqlExec); ok {
		return e.Exec(q, args...)
	}
	return nil, errNoSQLTx
}

func (t *leaderTx) Query(q string, args ...any) (*sql.Rows, error) {
	if e, ok := t.Tx.(sqlExec); ok {
		return e.Query(q, args...)
	}
	return nil, errNoSQLTx
}
