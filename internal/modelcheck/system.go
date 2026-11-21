package modelcheck

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/announce"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/gates"
	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/playbooks"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	repo  = "mc/repo"
	prURL = "https://example.test/pr/1"
)

var epoch = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

type ObsTask struct {
	Status   string
	Assignee string
	Reviewer string
	Rounds   int
	Leased   bool
	Expiries int
	Tests    Tests
	Block    Block
	Handoff  bool
	Previous uint8
}

type Obs struct {
	Paused      bool
	Closed      bool
	AgentPaused [3]bool
	Tasks       [NumTasks]ObsTask
}

func (s State) Observe() Obs {
	o := Obs{Paused: s.Paused, Closed: s.Closed, AgentPaused: s.AgentPaused}
	for i, t := range s.Tasks {
		ot := ObsTask{Status: t.Status, Assignee: t.Assignee.String(), Rounds: int(t.Rounds), Leased: t.Leased(),
			Expiries: int(t.Expiries), Tests: t.Tests}
		if t.Reviewed {
			ot.Reviewer = ReviewerID
		}
		if t.Status == Blocked {
			ot.Block = t.Block
		}
		ot.Handoff, ot.Previous = t.handoff()
		o.Tasks[i] = ot
	}
	return o
}

type Template struct {
	dir    string
	data   []byte
	seq    atomic.Int64
	keys   atomic.Int64
	briefs [2]string
	plans  [2]string
	tasks  [NumTasks]string
	tokens map[string]string

	auditBase int64
}

func agentIDs() []string {
	ids := []string{L1.String(), L2.String(), ReviewerID}
	for _, w := range Workers {
		ids = append(ids, w.String())
	}
	return ids
}

func NewTemplate(ctx context.Context, dir string) (*Template, error) {
	tp := &Template{dir: dir, tokens: map[string]string{}}
	sys, err := tp.Start()
	if err != nil {
		return nil, err
	}
	defer sys.Close()
	if err := tp.setup(ctx, sys); err != nil {
		return nil, err
	}
	if err := sys.st.View(ctx, func(tx store.Tx) (err error) {
		tp.auditBase, err = auditHead(tx)
		return err
	}); err != nil {
		return nil, err
	}
	snap := filepath.Join(dir, "template.db")
	if err := sys.st.Backup(ctx, snap); err != nil {
		return nil, err
	}
	if tp.data, err = os.ReadFile(filepath.Clean(snap)); err != nil {
		return nil, err
	}
	return tp, os.Remove(snap)
}

func (tp *Template) setup(ctx context.Context, sys *System) error {
	b := sys.b
	agents := []board.AgentInput{
		{ID: L1.String(), Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"},
		{ID: L2.String(), Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"},
		{ID: ReviewerID, Role: model.RoleReviewer, Kind: model.KindOther, Provider: ReviewerProvider},
	}
	for _, w := range Workers {
		agents = append(agents, board.AgentInput{ID: w.String(), Role: model.RoleWorker, Kind: model.KindOther, Provider: w.Provider()})
	}
	for _, a := range agents {
		r, err := b.RegisterAgent(ctx, a)
		if err != nil {
			return err
		}
		tp.tokens[a.ID] = r.Connect.Token
	}
	for _, w := range Workers {
		if q := w.MaxConcurrent(); q > 0 {
			if err := sys.quota.Set(ctx, w.String(), quota.Quota{MaxConcurrentTasks: q}); err != nil {
				return err
			}
		}
	}
	if err := (&playbooks.Store{St: sys.st, Now: func() time.Time { return sys.now }}).Seed(ctx); err != nil {
		return err
	}
	for bi, br := range []Brief{BriefA, BriefB} {
		in := board.BriefInput{Title: fmt.Sprint("modelcheck ", bi), Repos: []string{repo}, LeaderID: br.Leader().String()}
		if br == BriefA {
			in.MaxCostEUR = BriefMaxCost
		} else {
			in.Playbook = Playbook
		}
		created, err := b.CreateBrief(ctx, in)
		if err != nil {
			return err
		}
		plan := board.PlanInput{BriefID: created.ID}
		var idx []int
		for i, spec := range Tasks {
			if spec.Brief != br {
				continue
			}
			ti := board.TaskInput{Ref: fmt.Sprint("t", i), Title: fmt.Sprint("task ", i), Spec: "spec", Repo: repo,
				AcceptanceCriteria: []string{"works"}, Budget: budget(spec.Low)}
			for _, d := range spec.DependsOn {
				ti.DependsOn = append(ti.DependsOn, fmt.Sprint("t", d))
			}
			plan.Tasks = append(plan.Tasks, ti)
			idx = append(idx, i)
		}
		p, err := b.ProposePlan(ctx, actor(br.Leader().String(), model.RoleLeader), plan)
		if err != nil {
			return err
		}
		tp.briefs[bi], tp.plans[bi] = created.ID, p.ID
		for j, t := range p.Tasks {
			tp.tasks[idx[j]] = t.ID
		}
		if br == BriefB {
			if _, err := b.ApprovePlan(ctx, p.ID, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func budget(b Budget) model.Budget {
	return model.Budget{MaxMinutes: b.MaxMinutes, MaxCostEUR: float64(b.MaxCost)}
}

type call struct {
	agent, tool, key string
	args             map[string]any
}

type System struct {
	tp       *Template
	st       *sqlite.Store
	b        *board.Board
	quota    *quota.Service
	announce *announce.Service
	mcp      http.Handler
	clients  map[string]*client.MCP
	key      string
	last     *call
	lastRes  string
	path     string
	now      time.Time
	flap     bool
}

type Snapshot struct {
	now    time.Time
	audit  []auditRow
	tasks  []*model.Task
	briefs []*model.Brief
	plans  []*model.Plan
	agents []*model.Agent
	paused string
}

func (tp *Template) Start() (*System, error) { return tp.start(false) }

func (tp *Template) StartFlapping() (*System, error) { return tp.start(true) }

func (tp *Template) start(flap bool) (*System, error) {
	path := filepath.Join(tp.dir, fmt.Sprintf("mc-%d.db", tp.seq.Add(1)))
	if tp.data != nil {
		if err := os.WriteFile(path, tp.data, 0o600); err != nil {
			return nil, err
		}
	}
	st, err := sqlite.Open(path)
	if err != nil {
		return nil, err
	}
	sys := &System{tp: tp, st: st, path: path, now: epoch, flap: flap}
	err = st.Atomic(context.Background(), func(tx store.Tx) error {
		return execSQL(tx, `DROP TRIGGER IF EXISTS audit_no_delete`)
	})
	if err == nil {
		err = sys.reload(context.Background())
	}
	if err != nil {
		sys.Close()
		return nil, err
	}
	return sys, nil
}

type tokenLookup struct{ s *System }

func (l tokenLookup) LookupToken(tok string) (*model.Agent, error) {
	return l.s.b.LoadByTokenHash(auth.Hash(tok))
}

type audience struct{ b *board.Board }

func (a audience) Brief(ctx context.Context, id string) error {
	_, err := a.b.GetBrief(ctx, id)
	return err
}

func (a audience) BriefAgents(ctx context.Context, id string) ([]string, error) {
	return a.b.ActiveAgentsOfBrief(ctx, id)
}

func (a audience) AllAgents(context.Context) ([]string, error) { return agentIDs(), nil }

type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func (s *System) reload(ctx context.Context) error {
	clock := func() time.Time { return s.now }
	o := board.Options{
		Lease: LeaseMinutes * time.Minute,
		Now:   clock,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if !s.flap {
		o.FlapExpiries = 1 << 30
	}
	b, err := board.New(ctx, s.st, o)
	if err != nil {
		return err
	}
	s.b = b
	s.quota = quota.New(quota.Options{Store: s.st, Now: clock})
	b.SetGate(s.quota.Gate)
	b.SetQualityGate(gates.NewService(b, s.st, nil, clock).Gate)
	b.SetPlaybooks(playbooks.Lookup)
	s.announce = announce.New(announce.Config{Store: s.st, Audience: audience{b}, Poster: b, Now: clock})
	lookup := tokenLookup{s}
	srv := mcp.NewServer(lookup, mcp.NewRegistry(b))
	srv.Now = clock
	s.mcp = idempotency.Wrap(srv, idempotency.Options{Store: s.st, Agents: lookup, Now: clock, Log: o.Log})
	s.clients = map[string]*client.MCP{}
	s.last = nil
	return nil
}

func (s *System) Snapshot(ctx context.Context) (*Snapshot, error) {
	s.b.Flush(ctx)
	snap := &Snapshot{now: s.now}
	err := s.st.View(ctx, func(tx store.Tx) error {
		for _, id := range s.tp.tasks {
			t, err := tx.GetTask(id)
			if err != nil {
				return err
			}
			snap.tasks = append(snap.tasks, t)
		}
		for i := range s.tp.briefs {
			br, err := tx.GetBrief(s.tp.briefs[i])
			if err != nil {
				return err
			}
			p, err := tx.GetPlan(s.tp.plans[i])
			if err != nil {
				return err
			}
			snap.briefs, snap.plans = append(snap.briefs, br), append(snap.plans, p)
		}
		for _, id := range agentIDs() {
			a, err := tx.GetAgent(id)
			if err != nil {
				return err
			}
			snap.agents = append(snap.agents, a)
		}
		var err error
		if snap.audit, err = readAudit(tx, s.tp.auditBase); err != nil {
			return err
		}
		snap.paused, _, err = tx.GetKV("paused")
		return err
	})
	return snap, err
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("modelcheck: store transaction does not expose SQL")

type auditRow struct {
	id, ts                                          int64
	agent, role, tool, task, before, after, note, h string
}

const auditCols = `id, ts, agent_id, role, tool, task_id, before, after, note, hash`

func querySQL(tx store.Tx, q string, args []any, scan func(*sql.Rows) error) error {
	x, ok := tx.(sqlTx)
	if !ok {
		return errNoSQL
	}
	rows, err := x.Query(q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func auditHead(tx store.Tx) (int64, error) {
	var id int64
	err := querySQL(tx, `SELECT COALESCE(MAX(id), 0) FROM audit`, nil, func(r *sql.Rows) error { return r.Scan(&id) })
	return id, err
}

func readAudit(tx store.Tx, after int64) ([]auditRow, error) {
	var out []auditRow
	err := querySQL(tx, `SELECT `+auditCols+` FROM audit WHERE id > ? ORDER BY id`, []any{after}, func(r *sql.Rows) error {
		var a auditRow
		if err := r.Scan(&a.id, &a.ts, &a.agent, &a.role, &a.tool, &a.task, &a.before, &a.after, &a.note, &a.h); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (s *System) restoreAudit(tx store.Tx, rows []auditRow) error {
	base := s.tp.auditBase
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		var h string
		if err := querySQL(tx, `SELECT hash FROM audit WHERE id = ?`, []any{last.id}, func(r *sql.Rows) error { return r.Scan(&h) }); err != nil {
			return err
		}
		if h == last.h {
			return execSQL(tx, `DELETE FROM audit WHERE id > ?`, last.id)
		}
	}
	if err := execSQL(tx, `DELETE FROM audit WHERE id > ?`, base); err != nil {
		return err
	}
	for _, a := range rows {
		if err := execSQL(tx, `INSERT INTO audit(`+auditCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			a.id, a.ts, a.agent, a.role, a.tool, a.task, a.before, a.after, a.note, a.h); err != nil {
			return err
		}
	}
	return nil
}

func execSQL(tx store.Tx, q string, args ...any) error {
	x, ok := tx.(sqlTx)
	if !ok {
		return errNoSQL
	}
	_, err := x.Exec(q, args...)
	return err
}

func (s *System) Restore(ctx context.Context, snap *Snapshot) error {
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		for _, t := range snap.tasks {
			if err := restoreRow(func() (*model.Task, error) { return tx.GetTask(t.ID) }, tx.UpdateTask, t); err != nil {
				return err
			}
		}
		for i, br := range snap.briefs {
			if err := restoreRow(func() (*model.Brief, error) { return tx.GetBrief(br.ID) }, tx.UpdateBrief, br); err != nil {
				return err
			}
			p := snap.plans[i]
			if err := restoreRow(func() (*model.Plan, error) { return tx.GetPlan(p.ID) }, tx.UpdatePlan, p); err != nil {
				return err
			}
		}
		for _, a := range snap.agents {
			if err := restoreRow(func() (*model.Agent, error) { return tx.GetAgent(a.ID) }, tx.UpdateAgent, a); err != nil {
				return err
			}
		}
		if err := s.restoreAudit(tx, snap.audit); err != nil {
			return err
		}
		return tx.SetKV("paused", snap.paused)
	})
	if err != nil {
		return err
	}
	s.now = snap.now
	return s.reload(ctx)
}

func restoreRow[T any](get func() (*T, error), put func(*T) error, want *T) error {
	cur, err := get()
	if err != nil || reflect.DeepEqual(cur, want) {
		return err
	}
	cp := *want
	return put(&cp)
}

func (s *System) Close() {
	_ = s.st.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(s.path + suffix)
	}
}

func actor(id, role string) model.Actor { return model.Actor{ID: id, Role: role} }

func (s *System) client(agent string) *client.MCP {
	if c, ok := s.clients[agent]; ok {
		return c
	}
	c := client.NewMCP("http://modelcheck/mcp", s.tp.tokens[agent],
		client.WithHTTPClient(&http.Client{Transport: inProcess{s.mcp}}),
		client.WithSleep(func(context.Context, time.Duration) error { return nil }),
		client.WithIdempotencyKeys(func() string { return s.key }))
	s.clients[agent] = c
	return c
}

func (s *System) send(ctx context.Context, c *call) (string, error) {
	s.key = c.key
	var raw []byte
	err := s.client(c.agent).Call(ctx, c.tool, c.args, &rawJSON{&raw})
	var ce *client.Error
	if errors.As(err, &ce) {
		return ce.Code + ":" + ce.Message, &model.Error{Code: ce.Code, Message: ce.Message}
	}
	return string(raw), err
}

type rawJSON struct{ b *[]byte }

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r.b = bytes.Clone(b)
	return nil
}

func (s *System) callMCP(ctx context.Context, agent, tool string, args map[string]any) error {
	c := &call{agent: agent, tool: tool, args: args, key: fmt.Sprint("mc-", s.tp.keys.Add(1))}
	res, err := s.send(ctx, c)
	s.last, s.lastRes = c, res
	return err
}

func (s *System) Retry(ctx context.Context) (string, bool, error) {
	if s.last == nil {
		return "", false, errors.New("no idempotent call to retry")
	}
	res, err := s.send(ctx, s.last)
	return code(err), res == s.lastRes, nil
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return model.AsError(err).Code
}

func (s *System) Do(ctx context.Context, from State, a Action) string {
	b, tp := s.b, s.tp
	lead := actor(a.Lead.String(), model.RoleLeader)
	tid := tp.tasks[a.Task]
	wid := from.Actor(a).String()
	w := actor(wid, model.RoleWorker)
	status := func(st string) *string { return &st }
	var err error
	switch a.Kind {
	case Assign:
		_, err = b.Assign(ctx, lead, tid, a.Agent.String(), "assignee")
	case Reassign:
		_, err = b.Reassign(ctx, lead, tid, a.Agent.String(), "modelcheck")
	case Claim:
		err = s.callMCP(ctx, wid, "claim_task", map[string]any{"task_id": tid})
	case Release:
		_, err = b.ReleaseTask(ctx, w, tid, "modelcheck")
	case Submit:
		err = s.callMCP(ctx, wid, "submit", map[string]any{"task_id": tid, "pr_url": prURL, "summary": "done"})
	case BlockSelf:
		_, err = b.UpdateStatus(ctx, w, board.StatusInput{TaskID: tid, Status: model.StatusBlocked, Note: "stuck"})
	case ReportCost:
		c := float64(ReportedCost)
		_, err = b.UpdateStatus(ctx, w, board.StatusInput{TaskID: tid, Status: model.StatusInProgress, CostEUR: &c})
	case TestsPassed:
		_, err = b.ReportTests(ctx, w, tid, 3, 0, "")
	case TestsFailed:
		_, err = b.ReportTests(ctx, w, tid, 2, 1, "")
	case ReviewApprove:
		err = s.callMCP(ctx, ReviewerID, "review", map[string]any{"task_id": tid, "verdict": "approve", "comments": "ok"})
	case ReviewChanges:
		err = s.callMCP(ctx, ReviewerID, "review", map[string]any{"task_id": tid, "verdict": "changes_requested", "comments": "fix it"})
	case AssignReviewer:
		_, err = b.Assign(ctx, lead, tid, ReviewerID, "reviewer")
	case MarkDone:
		_, err = b.MarkDone(ctx, tid, "merged")
	case Cancel:
		_, err = b.Cancel(ctx, tid, "modelcheck")
	case ReopenReady:
		_, err = b.Reopen(ctx, tid, model.StatusReady, "")
	case ReopenAssigned:
		_, err = b.Reopen(ctx, tid, model.StatusAssigned, "")
	case LeaderBlock:
		_, err = b.UpdateTask(ctx, lead, tid, board.TaskPatch{Status: status(model.StatusBlocked)})
	case LeaderReady:
		_, err = b.UpdateTask(ctx, lead, tid, board.TaskPatch{Status: status(model.StatusReady)})
	case LeaderCancel:
		_, err = b.UpdateTask(ctx, lead, tid, board.TaskPatch{Status: status(model.StatusCancelled)})
	case ToggleBudget:
		bud := Tasks[a.Task].High
		if from.Tasks[a.Task].HighBudget {
			bud = Tasks[a.Task].Low
		}
		_, err = b.SetTaskBudget(ctx, tid, budget(bud))
	case PromoteGated:
		_, err = b.PromoteGated(ctx, tid)
	case Heartbeat:
		_, err = b.Heartbeat(ctx, actor(a.Agent.String(), model.RoleWorker), "")
	case ResumeAgent:
		_, err = b.ResumeAgent(ctx, a.Agent.String())
	case Tick:
		s.now = s.now.Add(TickMinutes * time.Minute)
		b.Flush(ctx)
		err = b.Sweep(ctx)
	case PauseAll:
		err = b.PauseAll(ctx, "modelcheck")
	case ResumeAll:
		err = b.ResumeAll(ctx)
	case ApprovePlan:
		_, err = b.ApprovePlan(ctx, tp.plans[BriefA], "")
	case ToggleCrossReview:
		cr := from.NoCrossReview
		_, err = b.UpdateBrief(ctx, tp.briefs[BriefA], board.BriefPatch{CrossReview: &cr})
	case DisablePlaybookReview:
		off := false
		_, err = b.UpdateBrief(ctx, tp.briefs[BriefB], board.BriefPatch{CrossReview: &off})
	case ToggleGates:
		q := model.QualityGates{Tests: !from.Gates}
		_, err = b.UpdateBrief(ctx, tp.briefs[BriefA], board.BriefPatch{QualityGates: &q})
	case CloseBrief:
		_, err = b.UpdateBrief(ctx, tp.briefs[BriefA], board.BriefPatch{Status: status("closed")})
	case ReopenBrief:
		_, err = b.UpdateBrief(ctx, tp.briefs[BriefA], board.BriefPatch{Status: status("open")})
	case Announce:
		_, _, err = s.announce.Publish(ctx, announce.Input{Text: "heads up", BriefID: tp.briefs[BriefA]})
	}
	return code(err)
}

func blockOf(reason string) Block {
	switch {
	case reason == "blocked by leader":
		return BlockLeader
	case strings.HasPrefix(reason, "blocked by agent"):
		return BlockAgent
	case strings.HasPrefix(reason, "budget_exceeded"):
		return BlockBudget
	case strings.HasPrefix(reason, "poison_task"):
		return BlockPoison
	case reason == "brief_closed":
		return BlockClosed
	}
	return -1
}

func testsOf(r *model.TestResult) Tests {
	switch {
	case r == nil:
		return TestsNone
	case r.Failed > 0:
		return TestsFail
	}
	return TestsPass
}

func workerBit(id string) uint8 {
	for _, w := range Workers {
		if w.String() == id {
			return w.bit()
		}
	}
	return 1 << 7
}

func (s *System) Observe(ctx context.Context) (Obs, error) {
	p, _ := s.b.Paused()
	o := Obs{Paused: p}
	idx := map[string]int{}
	for i, id := range s.tp.tasks {
		idx[id] = i
	}
	paged := map[string]string{}
	for bi, briefID := range s.tp.briefs {
		ts, err := s.listPaged(ctx, briefID)
		if err != nil {
			return o, err
		}
		for _, t := range ts {
			i, ok := idx[t.ID]
			if !ok || Tasks[i].Brief != Brief(bi) {
				return o, fmt.Errorf("brief %s lists unexpected task %s", briefID, t.ID)
			}
			if _, dup := paged[t.ID]; dup {
				return o, fmt.Errorf("brief %s lists task %s twice across pages", briefID, t.ID)
			}
			paged[t.ID] = t.Status
			if err := s.observeTask(ctx, &o.Tasks[i], t); err != nil {
				return o, err
			}
		}
	}
	if err := s.checkLeaderBoards(ctx, paged); err != nil {
		return o, err
	}
	br, err := s.b.GetBrief(ctx, s.tp.briefs[BriefA])
	if err != nil {
		return o, err
	}
	o.Closed = br.Status == "closed"
	ags, err := s.b.ListAgents(ctx, model.RoleWorker, "")
	if err != nil {
		return o, err
	}
	for _, ag := range ags {
		for _, w := range Workers {
			if ag.ID == w.String() {
				o.AgentPaused[w] = ag.Status == model.AgentPaused
			}
		}
	}
	return o, nil
}

func (s *System) observeTask(ctx context.Context, ot *ObsTask, t *model.Task) error {
	*ot = ObsTask{Status: t.Status, Rounds: t.ReviewRounds, Leased: t.LeaseExpiresAt != nil, Expiries: t.LeaseExpiries, Tests: testsOf(t.Tests)}
	if t.Status == model.StatusBlocked {
		ot.Block = blockOf(*t.BlockedReason)
	}
	if t.Reviewer != nil {
		ot.Reviewer = *t.Reviewer
	}
	if t.Assignee == nil {
		return nil
	}
	ot.Assignee = *t.Assignee
	hc, err := s.b.HandoffContext(ctx, actor(*t.Assignee, model.RoleWorker), t)
	if err != nil || hc == nil {
		return err
	}
	ot.Handoff = true
	for _, id := range hc.PreviousAssignees {
		ot.Previous |= workerBit(id)
	}
	return nil
}

func (s *System) listPaged(ctx context.Context, briefID string) ([]*model.Task, error) {
	var out []*model.Task
	q := board.TaskQuery{BriefID: briefID, Limit: 1}
	for {
		p, err := s.b.ListTasksPage(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.Next == "" {
			return out, nil
		}
		if len(out) > NumTasks {
			return nil, fmt.Errorf("brief %s: paging does not terminate", briefID)
		}
		q.Cursor = p.Next
	}
}

func (s *System) checkLeaderBoards(ctx context.Context, paged map[string]string) error {
	for _, l := range []Leader{L1, L2} {
		v, err := s.b.BoardFor(ctx, actor(l.String(), model.RoleLeader), "")
		if err != nil {
			return err
		}
		got := map[string]string{}
		for st, ts := range v.Columns {
			for _, t := range ts {
				got[t.ID] = st
			}
		}
		want := map[string]string{}
		for i, id := range s.tp.tasks {
			if Tasks[i].Brief.Leader() == l {
				want[id] = paged[id]
			}
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("unpaged board of %s shows %v, paged listing of its brief %v", l, got, want)
		}
	}
	return nil
}
