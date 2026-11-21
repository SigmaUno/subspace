package board

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	DefaultPoisonExpiries = 3
	DefaultFlapExpiries   = 3
	DefaultFlapWindow     = 30 * time.Minute
)

type Options struct {
	PublicURL  string
	Lease      time.Duration
	Capacity   capacity.Config
	Spawner    spawner.Spawner
	Hub        events.Publisher
	Invalidate func(agentID string)
	Now        func() time.Time
	Log        *slog.Logger
	Backups    *backup.Scheduler

	PoisonExpiries int
	FlapExpiries   int
	FlapWindow     time.Duration

	FlushEvery time.Duration
	SweepEvery time.Duration
}

type hotTask struct {
	hb, lease time.Time
	assignee  string
}

type Board struct {
	st  store.Store
	o   Options
	log *slog.Logger

	mu      sync.Mutex
	seen    map[string]time.Time
	tasks   map[string]hotTask
	dirtyA  map[string]struct{}
	dirtyT  map[string]struct{}
	capCfg  capacity.Config
	capGate capacity.Gate
	gate    Gate

	expiries map[string][]time.Time

	pausedMu sync.RWMutex
	paused   bool
	pauseWhy string
}

func New(ctx context.Context, st store.Store, o Options) (*Board, error) {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Lease <= 0 {
		o.Lease = 15 * time.Minute
	}
	if o.PoisonExpiries <= 0 {
		o.PoisonExpiries = DefaultPoisonExpiries
	}
	if o.FlapExpiries <= 0 {
		o.FlapExpiries = DefaultFlapExpiries
	}
	if o.FlapWindow <= 0 {
		o.FlapWindow = DefaultFlapWindow
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 2 * time.Second
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Spawner == nil {
		o.Spawner = noopSpawner{}
	}
	if o.PublicURL == "" {
		o.PublicURL = "http://localhost:8080"
	}
	b := &Board{st: st, o: o, log: o.Log, seen: map[string]time.Time{}, tasks: map[string]hotTask{},
		dirtyA: map[string]struct{}{}, dirtyT: map[string]struct{}{}, capCfg: o.Capacity,
		expiries: map[string][]time.Time{}}
	err := st.View(ctx, func(tx store.Tx) error {
		if v, ok, err := tx.GetKV("paused"); err != nil {
			return err
		} else if ok && v != "" {
			b.paused, b.pauseWhy = true, v
		}
		if v, ok, err := tx.GetKV("capacity"); err != nil {
			return err
		} else if ok {
			var c capacity.Config
			if json.Unmarshal([]byte(v), &c) == nil {
				b.capCfg = c
			}
		}
		return nil
	})
	return b, err
}

func (b *Board) now() time.Time { return b.o.Now() }

type wctx struct {
	b      *Board
	events []pending
	inval  []string
}

type pending struct {
	topics []string
	typ    string
	data   any
}

func (w *wctx) emit(topics []string, typ string, data any) {
	w.events = append(w.events, pending{topics, typ, data})
}

func (w *wctx) task(t *model.Task, created bool) {
	typ := "task.updated"
	if created {
		typ = "task.created"
	}
	cp := *t
	w.emit([]string{"board", "brief:" + t.BriefID, "task:" + t.ID}, typ, &cp)
}

func (w *wctx) agent(a *model.Agent) {
	cp := *a
	w.emit([]string{"agents", "agent:" + a.ID}, "agent.updated", &cp)
	w.inval = append(w.inval, a.ID)
}

func (w *wctx) post(tx store.Tx, kind, id string, author model.Actor, mk, body string) error {
	if len(body) > maxBody {
		return model.BadRequest("message too long (max %d bytes)", maxBody)
	}
	m := &model.Message{ThreadKind: kind, ThreadID: id, AuthorID: author.ID, AuthorRole: author.Role, Kind: mk, Body: body, CreatedAt: w.b.now()}
	if err := tx.InsertMessage(m); err != nil {
		return err
	}
	topics := []string{"thread:" + kind + ":" + id}
	if mk == model.MsgQuestion {
		topics = append(topics, "questions")
	}
	w.emit(topics, "message.created", m)
	return nil
}

func (w *wctx) audit(tx store.Tx, a model.Actor, tool, taskID string, before, after map[string]any, note string) error {
	e := &model.AuditEvent{TS: w.b.now(), AgentID: a.ID, Role: a.Role, Tool: tool, Before: before, After: after}
	if taskID != "" {
		e.TaskID = &taskID
	}
	if note != "" {
		e.Note = &note
	}
	return tx.InsertAudit(e)
}

func (b *Board) write(ctx context.Context, fn func(tx store.Tx, w *wctx) error) error {
	w := &wctx{b: b}
	if err := b.st.Atomic(ctx, func(tx store.Tx) error { w.events, w.inval = nil, nil; return fn(tx, w) }); err != nil {
		return mapStoreErr(err)
	}
	for _, id := range w.inval {
		if b.o.Invalidate != nil {
			b.o.Invalidate(id)
		}
	}
	if b.o.Hub != nil {
		for _, e := range w.events {
			b.o.Hub.Publish(e.topics, e.typ, e.data)
		}
	}
	return nil
}

func (b *Board) read(ctx context.Context, fn func(tx store.Tx) error) error {
	return mapStoreErr(b.st.View(ctx, fn))
}

func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case isErr(err, store.ErrNotFound):
		return model.NotFound("not found")
	case isErr(err, store.ErrDuplicate):
		return model.Conflict("already exists")
	}
	return err
}

func (b *Board) overlay(t *model.Task) *model.Task {
	if t.Status != model.StatusInProgress {
		return t
	}
	b.mu.Lock()
	h, ok := b.tasks[t.ID]
	b.mu.Unlock()
	if !ok {
		return t
	}
	cp := *t
	if t.LastHeartbeatAt == nil || h.hb.After(*t.LastHeartbeatAt) {
		hb := h.hb
		cp.LastHeartbeatAt = &hb
	}
	if t.LeaseExpiresAt == nil || h.lease.After(*t.LeaseExpiresAt) {
		l := h.lease
		cp.LeaseExpiresAt = &l
	}
	return &cp
}

func (b *Board) overlayAgent(a *model.Agent) *model.Agent {
	b.mu.Lock()
	s, ok := b.seen[a.ID]
	b.mu.Unlock()
	if !ok || (a.LastSeenAt != nil && !s.After(*a.LastSeenAt)) {
		return a
	}
	cp := *a
	cp.LastSeenAt = &s
	return &cp
}

func (b *Board) forget(taskID string) {
	b.mu.Lock()
	delete(b.tasks, taskID)
	delete(b.dirtyT, taskID)
	b.mu.Unlock()
}

func (b *Board) Touch(agentID string) {
	now := b.now()
	b.mu.Lock()
	b.seen[agentID] = now
	b.dirtyA[agentID] = struct{}{}
	b.mu.Unlock()
}

func (b *Board) Paused() (bool, string) {
	b.pausedMu.RLock()
	defer b.pausedMu.RUnlock()
	return b.paused, b.pauseWhy
}

func pausedTx(tx store.Tx) error {
	v, ok, err := tx.GetKV("paused")
	if err != nil {
		return err
	}
	if ok && v != "" {
		return model.Errf("paused", "Subspace is paused: %s", v)
	}
	return nil
}

func (b *Board) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { //nolint:gosec // background loop, lifetime bound to ctx
		defer wg.Done()
		t := time.NewTicker(b.o.FlushEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				b.Flush(context.Background())
				return
			case <-t.C:
				b.Flush(ctx)
			}
		}
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(b.o.SweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := b.Sweep(ctx); err != nil && ctx.Err() == nil {
					b.log.Error("sweep", "err", err)
				}
			}
		}
	}()
	wg.Wait()
}

func (b *Board) Flush(ctx context.Context) {
	b.mu.Lock()
	seen := make(map[string]int64, len(b.dirtyA))
	for id := range b.dirtyA {
		seen[id] = b.seen[id].UnixMilli()
	}
	rows := make([]store.LeaseRow, 0, len(b.dirtyT))
	for id := range b.dirtyT {
		h := b.tasks[id]
		rows = append(rows, store.LeaseRow{TaskID: id, Assignee: h.assignee, Heartbeat: h.hb.UnixMilli(), Lease: h.lease.UnixMilli()})
	}
	clear(b.dirtyA)
	clear(b.dirtyT)
	b.mu.Unlock()
	if len(seen) == 0 && len(rows) == 0 {
		return
	}
	err := b.st.Atomic(ctx, func(tx store.Tx) error {
		if len(seen) > 0 {
			if err := tx.TouchAgents(seen); err != nil {
				return err
			}
		}
		return tx.ExtendLeases(rows)
	})
	if err != nil {
		b.log.Error("flush heartbeats", "err", err)
		return
	}
	if b.o.Hub == nil {
		return
	}
	for _, row := range rows {
		var t *model.Task
		_ = b.read(ctx, func(tx store.Tx) (e error) { t, e = tx.GetTask(row.TaskID); return })
		if t != nil {
			b.o.Hub.Publish([]string{"board", "brief:" + t.BriefID, "task:" + t.ID}, "task.updated", b.overlay(t))
		}
	}
}
