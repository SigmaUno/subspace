package activity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	DefaultLimit = 100
	MaxLimit     = 1000

	batchSize = 200
	maxScan   = 5000
)

type Query struct {
	Since    time.Time
	Limit    int
	BriefID  string
	Severity string
	Cursor   string
}

type Page struct {
	Items      []Item  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type Service struct {
	st store.Store
}

func New(st store.Store) *Service { return &Service{st: st} }

type entry struct {
	Item
	seq int64
}

type stream struct {
	pos   int64
	buf   []entry
	done  bool
	fetch func(before int64) ([]entry, error)
}

func (s *stream) fill() error {
	if len(s.buf) > 0 || s.done {
		return nil
	}
	buf, err := s.fetch(s.pos)
	if err != nil {
		return err
	}
	s.buf, s.done = buf, len(buf) == 0
	return nil
}

func (s *Service) List(ctx context.Context, q Query) (Page, error) {
	if q.Severity != "" && !slices.Contains(Severities(), q.Severity) {
		return Page{}, model.BadRequest("severity must be one of %s", strings.Join(Severities(), ", "))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	auditPos, msgPos, err := parseCursor(q.Cursor)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: []Item{}}
	err = s.st.View(ctx, func(tx store.Tx) error {
		at, ok := tx.(store.ActivityTx)
		if !ok {
			return errors.New("store does not support activity")
		}
		res := newResolver(tx)
		audit := &stream{pos: auditPos, fetch: func(before int64) ([]entry, error) {
			evs, err := at.AuditNewest(store.ActivityRange{Before: before, Limit: batchSize})
			if err != nil {
				return nil, err
			}
			out := make([]entry, len(evs))
			for i, e := range evs {
				it := FromAudit(e)
				it.BriefID = res.auditBrief(e)
				out[i] = entry{it, e.ID}
			}
			return out, nil
		}}
		msgs := &stream{pos: msgPos, fetch: func(before int64) ([]entry, error) {
			ms, err := at.SystemMessagesNewest(store.ActivityRange{Before: before, Limit: batchSize})
			if err != nil {
				return nil, err
			}
			out := make([]entry, len(ms))
			for i, m := range ms {
				it := FromMessage(m)
				it.BriefID = res.messageBrief(m)
				out[i] = entry{it, m.ID}
			}
			return out, nil
		}}
		streams := []*stream{audit, msgs}
		for scanned := 0; len(page.Items) < limit && scanned < maxScan; scanned++ {
			var next *stream
			for _, st := range streams {
				if err := st.fill(); err != nil {
					return err
				}
				if len(st.buf) > 0 && (next == nil || newer(st.buf[0], next.buf[0])) {
					next = st
				}
			}
			if next == nil {
				break
			}
			e := next.buf[0]
			next.buf, next.pos = next.buf[1:], e.seq
			if !q.Since.IsZero() && !e.TS.After(q.Since) {
				next.buf, next.done = nil, true
				continue
			}
			if (q.BriefID == "" || e.BriefID == q.BriefID) && (q.Severity == "" || e.Severity == q.Severity) {
				page.Items = append(page.Items, e.Item)
			}
		}
		more := false
		for _, st := range streams {
			if err := st.fill(); err != nil {
				return err
			}
			more = more || len(st.buf) > 0
		}
		if more {
			c := fmt.Sprintf("a%d.m%d", audit.pos, msgs.pos)
			page.NextCursor = &c
		}
		return nil
	})
	return page, err
}

func newer(a, b entry) bool {
	if !a.TS.Equal(b.TS) {
		return a.TS.After(b.TS)
	}
	return a.Source == SourceAudit && b.Source != SourceAudit
}

func parseCursor(c string) (audit, msg int64, err error) {
	if c == "" {
		return math.MaxInt64, math.MaxInt64, nil
	}
	a, m, ok := strings.Cut(c, ".")
	a, aok := strings.CutPrefix(a, "a")
	m, mok := strings.CutPrefix(m, "m")
	if !ok || !aok || !mok {
		return 0, 0, model.BadRequest("invalid cursor")
	}
	audit, err = strconv.ParseInt(a, 10, 64)
	if err == nil {
		msg, err = strconv.ParseInt(m, 10, 64)
	}
	if err != nil || audit < 1 || msg < 1 {
		return 0, 0, model.BadRequest("invalid cursor")
	}
	return audit, msg, nil
}

type resolver struct {
	tx    store.Tx
	tasks map[string]string
	plans map[string]string
}

func newResolver(tx store.Tx) *resolver {
	return &resolver{tx: tx, tasks: map[string]string{}, plans: map[string]string{}}
}

func (r *resolver) taskBrief(id string) string {
	if id == "" {
		return ""
	}
	if b, ok := r.tasks[id]; ok {
		return b
	}
	var b string
	if t, err := r.tx.GetTask(id); err == nil {
		b = t.BriefID
	}
	r.tasks[id] = b
	return b
}

func (r *resolver) planBrief(id string) string {
	if id == "" {
		return ""
	}
	if b, ok := r.plans[id]; ok {
		return b
	}
	var b string
	if p, err := r.tx.GetPlan(id); err == nil {
		b = p.BriefID
	}
	r.plans[id] = b
	return b
}

func (r *resolver) auditBrief(e *model.AuditEvent) string {
	if id := format(e.After["brief_id"]); id != "" {
		return id
	}
	if e.TaskID != nil {
		if b := r.taskBrief(*e.TaskID); b != "" {
			return b
		}
	}
	return r.planBrief(format(e.After["plan_id"]))
}

func (r *resolver) messageBrief(m *model.Message) string {
	switch m.ThreadKind {
	case model.ThreadBrief:
		return m.ThreadID
	case model.ThreadTask:
		return r.taskBrief(m.ThreadID)
	}
	return ""
}
