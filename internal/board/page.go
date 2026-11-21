package board

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/threads"
)

const (
	taskPageDefault    = 1000
	agentPageDefault   = 100000
	auditPageDefault   = 200
	messagePageDefault = 200

	cursorTask     = "task"
	cursorAgent    = "agent"
	cursorBrief    = "brief"
	cursorPlan     = "plan"
	cursorQuestion = "question"
	cursorSpawn    = "spawn"
	cursorAudit    = "audit"
	cursorMessage  = "message"
)

type Page[T any] struct {
	Items []T
	Next  string
}

func encodeCursor(kind, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + id))
}

func decodeCursor(kind, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	id, ok := strings.CutPrefix(string(raw), kind+":")
	if err != nil || !ok || id == "" {
		return "", model.BadRequest("invalid cursor")
	}
	return id, nil
}

func decodeIntCursor(kind, cursor string, floor int64) (int64, error) {
	id, err := decodeCursor(kind, cursor)
	if err != nil || id == "" {
		return floor, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 0 {
		return 0, model.BadRequest("invalid cursor")
	}
	return max(n, floor), nil
}

func probe(limit int) int {
	if limit <= 0 {
		return 0
	}
	return limit + 1
}

func trim[T any](rows []T, limit int, kind string, id func(T) string) Page[T] {
	if limit <= 0 || len(rows) <= limit {
		return Page[T]{Items: rows}
	}
	rows = rows[:limit]
	return Page[T]{Items: rows, Next: encodeCursor(kind, id(rows[limit-1]))}
}

func (b *Board) ListTasksPage(ctx context.Context, q TaskQuery) (Page[*model.Task], error) {
	after, err := decodeCursor(cursorTask, q.Cursor)
	if err != nil {
		return Page[*model.Task]{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = taskPageDefault
	}
	f := store.TaskFilter{BriefID: q.BriefID, Assignee: q.Assignee, Role: q.Role, AfterID: after, Limit: probe(limit)}
	if q.Status != "" {
		f.Status = strings.Split(q.Status, ",")
	}
	var rows []*model.Task
	err = b.read(ctx, func(tx store.Tx) error {
		ts, err := tx.ListTasks(f)
		for _, t := range ts {
			rows = append(rows, b.overlay(t))
		}
		return err
	})
	p := trim(nz(rows), limit, cursorTask, func(t *model.Task) string { return t.ID })
	return p, err
}

func (b *Board) ListAgentsPage(ctx context.Context, role, status, cursor string, limit int) (Page[*model.Agent], error) {
	after, err := decodeCursor(cursorAgent, cursor)
	if err != nil {
		return Page[*model.Agent]{}, err
	}
	if limit <= 0 {
		limit = agentPageDefault
	}
	var rows []*model.Agent
	err = b.read(ctx, func(tx store.Tx) error {
		as, err := tx.ListAgents(store.AgentFilter{Role: role, Status: status, AfterID: after, Limit: probe(limit)})
		for _, a := range as {
			rows = append(rows, b.overlayAgent(a))
		}
		return err
	})
	return trim(nz(rows), limit, cursorAgent, func(a *model.Agent) string { return a.ID }), err
}

func (b *Board) ListBriefsPage(ctx context.Context, status, cursor string, limit int) (Page[*model.Brief], error) {
	after, err := decodeCursor(cursorBrief, cursor)
	if err != nil {
		return Page[*model.Brief]{}, err
	}
	var rows []*model.Brief
	err = b.read(ctx, func(tx store.Tx) error {
		bs, err := tx.ListBriefs(store.BriefFilter{Status: status, AfterID: after, Limit: probe(limit)})
		if err != nil {
			return err
		}
		for _, br := range bs {
			if br.CostUsed, err = tx.BriefCost(br.ID); err != nil {
				return err
			}
		}
		rows = bs
		return nil
	})
	return trim(nz(rows), limit, cursorBrief, func(br *model.Brief) string { return br.ID }), err
}

func (b *Board) ListPlansPage(ctx context.Context, briefID, status, cursor string, limit int) (Page[*model.Plan], error) {
	after, err := decodeCursor(cursorPlan, cursor)
	if err != nil {
		return Page[*model.Plan]{}, err
	}
	var rows []*model.Plan
	err = b.read(ctx, func(tx store.Tx) error {
		ps, err := tx.ListPlans(store.PlanFilter{BriefID: briefID, Status: status, AfterID: after, Limit: probe(limit)})
		if err != nil {
			return err
		}
		for _, p := range ps {
			if p, err = b.loadPlan(tx, p.ID); err != nil {
				return err
			}
			rows = append(rows, p)
		}
		return nil
	})
	return trim(nz(rows), limit, cursorPlan, func(p *model.Plan) string { return p.ID }), err
}

func (b *Board) ListQuestionsPage(ctx context.Context, status, cursor string, limit int) (Page[*model.Question], error) {
	after, err := decodeCursor(cursorQuestion, cursor)
	if err != nil {
		return Page[*model.Question]{}, err
	}
	var rows []*model.Question
	err = b.read(ctx, func(tx store.Tx) (e error) {
		rows, e = tx.ListQuestions(store.QuestionFilter{Status: status, AfterID: after, Limit: probe(limit)})
		return
	})
	return trim(nz(rows), limit, cursorQuestion, func(q *model.Question) string { return q.ID }), err
}

func (b *Board) ListSpawnsPage(ctx context.Context, briefID, cursor string, limit int) (Page[*model.Spawn], error) {
	after, err := decodeCursor(cursorSpawn, cursor)
	if err != nil {
		return Page[*model.Spawn]{}, err
	}
	var rows []*model.Spawn
	err = b.read(ctx, func(tx store.Tx) (e error) {
		rows, e = tx.ListSpawns(store.SpawnFilter{BriefID: briefID, AfterID: after, Limit: probe(limit)})
		return
	})
	return trim(nz(rows), limit, cursorSpawn, func(s *model.Spawn) string { return s.ID }), err
}

func (b *Board) ListAuditPage(ctx context.Context, f store.AuditFilter, cursor string) (Page[*model.AuditEvent], error) {
	since, err := decodeIntCursor(cursorAudit, cursor, f.SinceID)
	if err != nil {
		return Page[*model.AuditEvent]{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = auditPageDefault
	}
	f.SinceID, f.Limit = since, probe(limit)
	var rows []*model.AuditEvent
	err = b.read(ctx, func(tx store.Tx) (e error) { rows, e = tx.ListAudit(f); return })
	return trim(nz(rows), limit, cursorAudit, func(e *model.AuditEvent) string { return strconv.FormatInt(e.ID, 10) }), err
}

func (b *Board) OwnerThreadPage(ctx context.Context, kind, id, cursor string, since int64, limit int) (Page[*model.Message], error) {
	ref, err := threads.Parse(kind+":"+id, "")
	if err != nil {
		return Page[*model.Message]{}, err
	}
	return b.readRefPage(ctx, model.Owner, ref, cursor, since, limit)
}

func (b *Board) readRefPage(ctx context.Context, a model.Actor, ref threads.Ref, cursor string, since int64, limit int) (Page[*model.Message], error) {
	after, err := decodeIntCursor(cursorMessage, cursor, since)
	if err != nil {
		return Page[*model.Message]{}, err
	}
	switch {
	case limit <= 0:
		limit = messagePageDefault
	case limit > MaxPage:
		limit = MaxPage
	}
	var rows []*model.Message
	err = b.read(ctx, func(tx store.Tx) error {
		if err := canAccessThread(tx, a, ref); err != nil {
			return err
		}
		rows, err = tx.ListMessages(ref.Kind, ref.ID, after, probe(limit))
		return err
	})
	return trim(nz(rows), limit, cursorMessage, func(m *model.Message) string { return strconv.FormatInt(m.ID, 10) }), err
}
