package faultstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type Tx struct {
	s      *Store
	in     store.Tx
	ctx    context.Context
	cancel context.CancelFunc
	write  bool
}

var (
	_ store.Tx           = (*Tx)(nil)
	_ store.ActivityTx   = (*Tx)(nil)
	_ store.SearchTx     = (*Tx)(nil)
	_ store.TimeseriesTx = (*Tx)(nil)
	_ store.CostsTx      = (*Tx)(nil)
)

func (t *Tx) Inner() store.Tx { return t.in }

func (t *Tx) do(method string) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	k, d := t.s.next(method, t.write)
	switch k {
	case KindError:
		return fmt.Errorf("%w: %s", ErrInjected, method)
	case KindCancel:
		t.cancel()
		return t.ctx.Err()
	case KindPanic:
		panic(fmt.Sprintf("faultstore: injected panic in %s", method))
	case KindDelay:
		t.s.delay(d)
		return t.ctx.Err()
	}
	return nil
}

func (t *Tx) run(method string, fn func() error) error {
	if err := t.do(method); err != nil {
		return err
	}
	return fn()
}

func call[T any](t *Tx, method string, fn func() (T, error)) (T, error) {
	if err := t.do(method); err != nil {
		var zero T
		return zero, err
	}
	return fn()
}

func (t *Tx) NextID(prefix string) (string, error) {
	return call(t, "NextID", func() (string, error) { return t.in.NextID(prefix) })
}

func (t *Tx) InsertBrief(b *model.Brief) error {
	return t.run("InsertBrief", func() error { return t.in.InsertBrief(b) })
}

func (t *Tx) GetBrief(id string) (*model.Brief, error) {
	return call(t, "GetBrief", func() (*model.Brief, error) { return t.in.GetBrief(id) })
}

func (t *Tx) UpdateBrief(b *model.Brief) error {
	return t.run("UpdateBrief", func() error { return t.in.UpdateBrief(b) })
}

func (t *Tx) ListBriefs(f store.BriefFilter) ([]*model.Brief, error) {
	return call(t, "ListBriefs", func() ([]*model.Brief, error) { return t.in.ListBriefs(f) })
}

func (t *Tx) InsertPlan(p *model.Plan) error {
	return t.run("InsertPlan", func() error { return t.in.InsertPlan(p) })
}

func (t *Tx) GetPlan(id string) (*model.Plan, error) {
	return call(t, "GetPlan", func() (*model.Plan, error) { return t.in.GetPlan(id) })
}

func (t *Tx) UpdatePlan(p *model.Plan) error {
	return t.run("UpdatePlan", func() error { return t.in.UpdatePlan(p) })
}

func (t *Tx) ListPlans(f store.PlanFilter) ([]*model.Plan, error) {
	return call(t, "ListPlans", func() ([]*model.Plan, error) { return t.in.ListPlans(f) })
}

func (t *Tx) InsertTask(x *model.Task) error {
	return t.run("InsertTask", func() error { return t.in.InsertTask(x) })
}

func (t *Tx) GetTask(id string) (*model.Task, error) {
	return call(t, "GetTask", func() (*model.Task, error) { return t.in.GetTask(id) })
}

func (t *Tx) UpdateTask(x *model.Task) error {
	return t.run("UpdateTask", func() error { return t.in.UpdateTask(x) })
}

func (t *Tx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	return call(t, "ListTasks", func() ([]*model.Task, error) { return t.in.ListTasks(f) })
}

func (t *Tx) CountTasksByStatus() (map[string]int, error) {
	return call(t, "CountTasksByStatus", t.in.CountTasksByStatus)
}

func (t *Tx) BriefCost(briefID string) (float64, error) {
	return call(t, "BriefCost", func() (float64, error) { return t.in.BriefCost(briefID) })
}

func (t *Tx) InsertAgent(a *model.Agent) error {
	return t.run("InsertAgent", func() error { return t.in.InsertAgent(a) })
}

func (t *Tx) GetAgent(id string) (*model.Agent, error) {
	return call(t, "GetAgent", func() (*model.Agent, error) { return t.in.GetAgent(id) })
}

func (t *Tx) GetAgentByTokenHash(hash string) (*model.Agent, error) {
	return call(t, "GetAgentByTokenHash", func() (*model.Agent, error) { return t.in.GetAgentByTokenHash(hash) })
}

func (t *Tx) UpdateAgent(a *model.Agent) error {
	return t.run("UpdateAgent", func() error { return t.in.UpdateAgent(a) })
}

func (t *Tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	return call(t, "ListAgents", func() ([]*model.Agent, error) { return t.in.ListAgents(f) })
}

func (t *Tx) TouchAgents(seen map[string]int64) error {
	return t.run("TouchAgents", func() error { return t.in.TouchAgents(seen) })
}

func (t *Tx) ExtendLeases(rows []store.LeaseRow) error {
	return t.run("ExtendLeases", func() error { return t.in.ExtendLeases(rows) })
}

func (t *Tx) InsertMessage(m *model.Message) error {
	return t.run("InsertMessage", func() error { return t.in.InsertMessage(m) })
}

func (t *Tx) ListMessages(kind, id string, sinceID int64, limit int) ([]*model.Message, error) {
	return call(t, "ListMessages", func() ([]*model.Message, error) { return t.in.ListMessages(kind, id, sinceID, limit) })
}

func (t *Tx) InsertQuestion(q *model.Question) error {
	return t.run("InsertQuestion", func() error { return t.in.InsertQuestion(q) })
}

func (t *Tx) GetQuestion(id string) (*model.Question, error) {
	return call(t, "GetQuestion", func() (*model.Question, error) { return t.in.GetQuestion(id) })
}

func (t *Tx) UpdateQuestion(q *model.Question) error {
	return t.run("UpdateQuestion", func() error { return t.in.UpdateQuestion(q) })
}

func (t *Tx) ListQuestions(f store.QuestionFilter) ([]*model.Question, error) {
	return call(t, "ListQuestions", func() ([]*model.Question, error) { return t.in.ListQuestions(f) })
}

func (t *Tx) InsertAudit(e *model.AuditEvent) error {
	return t.run("InsertAudit", func() error { return t.in.InsertAudit(e) })
}

func (t *Tx) ListAudit(f store.AuditFilter) ([]*model.AuditEvent, error) {
	return call(t, "ListAudit", func() ([]*model.AuditEvent, error) { return t.in.ListAudit(f) })
}

func (t *Tx) AuditHead(maxID int64) (model.AuditHead, error) {
	return call(t, "AuditHead", func() (model.AuditHead, error) { return t.in.AuditHead(maxID) })
}

func (t *Tx) VerifyAudit() (model.AuditCheck, error) {
	return call(t, "VerifyAudit", t.in.VerifyAudit)
}

func (t *Tx) InsertSpawn(sp *model.Spawn) error {
	return t.run("InsertSpawn", func() error { return t.in.InsertSpawn(sp) })
}

func (t *Tx) UpdateSpawn(sp *model.Spawn) error {
	return t.run("UpdateSpawn", func() error { return t.in.UpdateSpawn(sp) })
}

func (t *Tx) ListSpawns(f store.SpawnFilter) ([]*model.Spawn, error) {
	return call(t, "ListSpawns", func() ([]*model.Spawn, error) { return t.in.ListSpawns(f) })
}

func (t *Tx) GetKV(key string) (string, bool, error) {
	if err := t.do("GetKV"); err != nil {
		return "", false, err
	}
	return t.in.GetKV(key)
}

func (t *Tx) SetKV(key, value string) error {
	return t.run("SetKV", func() error { return t.in.SetKV(key, value) })
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

const (
	noSQL        = "does not expose SQL"
	noActivity   = "has no activity queries"
	noSearch     = "has no search"
	noTimeseries = "has no timeseries"
	noCosts      = "has no costs"
)

func callOn[I, T any](t *Tx, method, missing string, fn func(I) (T, error)) (T, error) {
	in, ok := t.in.(I)
	if !ok {
		var zero T
		return zero, fmt.Errorf("faultstore: %T %s", t.in, missing)
	}
	return call(t, method, func() (T, error) { return fn(in) })
}

func runOn[I any](t *Tx, method, missing string, fn func(I) error) error {
	_, err := callOn(t, method, missing, func(in I) (struct{}, error) { return struct{}{}, fn(in) })
	return err
}

func (t *Tx) Exec(q string, args ...any) (sql.Result, error) {
	return callOn(t, "Exec", noSQL, func(in sqlTx) (sql.Result, error) { return in.Exec(q, args...) })
}

func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) {
	return callOn(t, "Query", noSQL, func(in sqlTx) (*sql.Rows, error) { return in.Query(q, args...) })
}

func (t *Tx) AuditNewest(r store.ActivityRange) ([]*model.AuditEvent, error) {
	return callOn(t, "AuditNewest", noActivity, func(in store.ActivityTx) ([]*model.AuditEvent, error) { return in.AuditNewest(r) })
}

func (t *Tx) SystemMessagesNewest(r store.ActivityRange) ([]*model.Message, error) {
	return callOn(t, "SystemMessagesNewest", noActivity, func(in store.ActivityTx) ([]*model.Message, error) {
		return in.SystemMessagesNewest(r)
	})
}

func (t *Tx) Search(q store.SearchQuery) ([]store.SearchHit, error) {
	return callOn(t, "Search", noSearch, func(in store.SearchTx) ([]store.SearchHit, error) { return in.Search(q) })
}

func (t *Tx) Timeseries(q store.TimeseriesQuery) ([]store.TimeseriesPoint, error) {
	return callOn(t, "Timeseries", noTimeseries, func(in store.TimeseriesTx) ([]store.TimeseriesPoint, error) { return in.Timeseries(q) })
}

func (t *Tx) PutUsage(r store.UsageRecord) error {
	return runOn(t, "PutUsage", noTimeseries, func(in store.TimeseriesTx) error { return in.PutUsage(r) })
}

func (t *Tx) PutCost(r store.CostRecord) error {
	return runOn(t, "PutCost", noCosts, func(in store.CostsTx) error { return in.PutCost(r) })
}

func (t *Tx) Costs(q store.CostQuery) ([]store.CostRow, error) {
	return callOn(t, "Costs", noCosts, func(in store.CostsTx) ([]store.CostRow, error) { return in.Costs(q) })
}

func (t *Tx) ListCosts(since, until time.Time) ([]store.CostRecord, error) {
	return callOn(t, "ListCosts", noCosts, func(in store.CostsTx) ([]store.CostRecord, error) { return in.ListCosts(since, until) })
}
