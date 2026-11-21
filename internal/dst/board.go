package dst

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type BoardSystem struct {
	path  string
	lease time.Duration
	clock *Clock
	hub   *recorder
	world World
	brief string

	st    *sqlite.Store
	b     *board.Board
	cache *auth.Cache

	mu     sync.Mutex
	tokens map[string][]string
	audit  []*model.AuditEvent
	last   *State
	dirty  map[string]bool
	steps  int
}

func (s *BoardSystem) touch(op Op) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mark := func(cs ...string) {
		for _, c := range cs {
			s.dirty[c] = true
		}
	}
	switch op.Kind {
	case OpRestart:
		mark("tasks", "agents", "plans", "spawns", "capacity")
	case OpHeartbeat, OpFlush:
		mark("tasks")
	case OpRegister, OpBulk, OpRotate:
		mark("agents")
	case OpSpawn, OpAutopilotTick:
		mark("agents", "spawns")
	case OpApprove, OpReject, OpPropose:
		mark("plans")
	case OpSetCapacity:
		mark("capacity")
	case OpHandoff:
		mark("briefs")
	}
}

func NewBoardSystem(dir string, clock *Clock, lease time.Duration) (System, error) {
	return newBoardSystem(dir, clock, lease, nil)
}

func newBoardSystem(dir string, clock *Clock, lease time.Duration, also events.Publisher) (*BoardSystem, error) {
	s := &BoardSystem{path: filepath.Join(dir, "dst.db"), lease: lease, clock: clock, hub: newRecorder(also), tokens: map[string][]string{}, dirty: map[string]bool{}}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *BoardSystem) open() error {
	st, err := sqlite.Open(s.path)
	if err != nil {
		return err
	}
	var cache *auth.Cache
	b, err := board.New(context.Background(), st, board.Options{
		Lease:   s.lease,
		Now:     s.clock.Now,
		Hub:     s.hub,
		Log:     discard,
		Spawner: simSpawner{s},
		Invalidate: func(id string) {
			if cache != nil {
				cache.Invalidate(id)
			}
		},
	})
	if err != nil {
		_ = st.Close()
		return err
	}
	cache = auth.NewCache(b.LoadByTokenHash, 30*time.Second)
	s.st, s.b, s.cache = st, b, cache
	return nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type simSpawner struct{ s *BoardSystem }

func (simSpawner) Name() string { return "dst" }

func (p simSpawner) Spawn(_ context.Context, r spawner.Request) error {
	p.s.issue(r.AgentID, r.Token)
	return nil
}

func (simSpawner) Stop(context.Context, string) error { return spawner.ErrNotImplemented }

func (simSpawner) Status(context.Context, string) (spawner.Status, error) {
	return spawner.StatusUnknown, nil
}

func (s *BoardSystem) issue(id, tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[id] = []string{tok}
}

func (s *BoardSystem) restart(crash bool) error {
	if !crash {
		s.b.Flush(context.Background())
	}
	herr := s.hub.reset()
	if err := s.st.Close(); err != nil {
		return err
	}
	if err := s.open(); err != nil {
		return err
	}
	return herr
}

func prURL(taskID string) string {
	return "https://git.example/" + simRepo + "/pull/" + strings.TrimPrefix(taskID, "T-")
}

const simRepo = "sim/repo"

func (s *BoardSystem) Setup(ctx context.Context, w World) (Setup, error) {
	var out Setup
	s.world = w
	br, err := s.b.CreateBrief(ctx, board.BriefInput{Title: "dst", Body: "simulated brief", Repos: []string{simRepo},
		MaxCostEUR: w.BriefCost, MaxParallel: w.MaxParallel, AllowedProviders: w.AllowedProviders})
	if err != nil {
		return out, err
	}
	s.brief = br.ID
	agents := append([]AgentSpec{{ID: w.Leader, Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"}}, w.Agents...)
	if w.Rival != "" {
		agents = append(agents, AgentSpec{ID: w.Rival, Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"})
	}
	for _, a := range agents {
		r, err := s.b.RegisterAgent(ctx, board.AgentInput{ID: a.ID, Role: a.Role, Kind: a.Kind, Provider: a.Provider, Model: "sim"})
		if err != nil {
			return out, err
		}
		s.tokens[a.ID] = []string{r.Connect.Token}
	}
	if w.Rival != "" {
		if _, err := s.b.SetBriefLeader(ctx, br.ID, w.Leader); err != nil {
			return out, err
		}
	}
	p, err := s.propose(ctx, model.Actor{ID: w.Leader, Role: model.RoleLeader})
	if err != nil {
		return out, err
	}
	out.PlanID, out.BriefID = p.ID, br.ID
	for _, t := range p.Tasks {
		out.TaskIDs = append(out.TaskIDs, t.ID)
	}
	return out, nil
}

func (s *BoardSystem) propose(ctx context.Context, a model.Actor) (*model.Plan, error) {
	in := board.PlanInput{BriefID: s.brief}
	for _, t := range s.world.Tasks {
		in.Tasks = append(in.Tasks, board.TaskInput{Ref: t.Ref, Title: "task " + t.Ref, Spec: "spec " + t.Ref, Repo: simRepo,
			Role: t.Role, DependsOn: t.DependsOn, Budget: t.Budget})
	}
	return s.b.ProposePlan(ctx, a, in)
}

func (s *BoardSystem) authenticate(op Op) (model.Actor, error) {
	s.mu.Lock()
	toks := s.tokens[op.Actor]
	tok := ""
	if op.Token >= 0 && op.Token < len(toks) {
		tok = toks[op.Token]
	}
	s.mu.Unlock()
	a, err := s.cache.LookupToken(tok)
	if err != nil {
		return model.Actor{}, err
	}
	if a == nil || a.Status == model.AgentRevoked {
		return model.Actor{}, model.Errf("unauthorized", "invalid token for %s", op.Actor)
	}
	if a.Status == model.AgentPaused && op.Kind != OpHeartbeat {
		return model.Actor{}, model.Errf("agent_paused", "agent %s is paused", a.ID)
	}
	return model.Actor{ID: a.ID, Role: a.Role}, nil
}

func (s *BoardSystem) Do(ctx context.Context, op Op) Result {
	s.touch(op)
	switch op.Kind {
	case OpBatch:
		return s.batch(ctx, op.Sub)
	case OpRestart:
		return Result{Err: s.restart(op.Crash)}
	}
	var a model.Actor
	if op.Authenticated() {
		var err error
		if a, err = s.authenticate(op); err != nil {
			return Result{Err: err}
		}
	}
	var err error
	switch op.Kind {
	case OpAdvance:
		s.clock.Advance(op.Dur)
	case OpSweep:
		err = s.b.Sweep(ctx)
	case OpFlush:
		s.b.Flush(ctx)
	case OpSubscribe:
		s.hub.subscribe([]string{op.Target})
	case OpUnsubscribe:
		err = s.hub.unsubscribe(op.N)
	case OpDrain:
		s.hub.drain(op.N, op.Limit)
	case OpApprove:
		_, err = s.b.ApprovePlan(ctx, op.Target, "")
	case OpReject:
		_, err = s.b.RejectPlan(ctx, op.Target, "dst")
	case OpPauseAll:
		err = s.b.PauseAll(ctx, "dst")
	case OpResumeAll:
		err = s.b.ResumeAll(ctx)
	case OpMarkDone:
		_, err = s.b.MarkDone(ctx, op.Task, "merged")
	case OpCancel:
		_, err = s.b.Cancel(ctx, op.Task, "dst")
	case OpReopen:
		_, err = s.b.Reopen(ctx, op.Task, model.StatusAssigned, "")
	case OpAnswer:
		_, err = s.b.Answer(ctx, op.Target, "yes")
	case OpRotate:
		var r *board.Registered
		if r, err = s.b.RotateToken(ctx, op.Target); err == nil {
			s.mu.Lock()
			s.tokens[op.Target] = append(s.tokens[op.Target], r.Connect.Token)
			s.mu.Unlock()
		}
	case OpRegister:
		var r *board.Registered
		if r, err = s.b.RegisterAgent(ctx, board.AgentInput{ID: op.Target, Role: model.RoleWorker, Kind: model.KindCodex, Model: "sim"}); err == nil {
			s.issue(r.Agent.ID, r.Connect.Token)
		}
	case OpBulk:
		var rs []*board.Registered
		if rs, err = s.b.RegisterBulk(ctx, board.BulkInput{Prefix: op.Target, Count: op.N, Role: model.RoleWorker, Kind: model.KindCodex, Model: "sim"}); err != nil {
			rs = nil
		}
		for _, r := range rs {
			s.issue(r.Agent.ID, r.Connect.Token)
		}
	case OpSetCapacity:
		_, err = s.b.SetCapacity(ctx, op.capacityConfig())
	case OpPropose:
		_, err = s.propose(ctx, a)
	case OpHandoff:
		_, err = s.b.SetBriefLeader(ctx, s.brief, op.Target)
	case OpAssign:
		_, err = s.b.Assign(ctx, a, op.Task, op.Target, "assignee")
	case OpAssignReviewer:
		_, err = s.b.Assign(ctx, a, op.Task, op.Target, "reviewer")
	case OpReassign:
		_, err = s.b.Reassign(ctx, a, op.Task, op.Target, "dst")
	case OpSpawn:
		_, err = s.b.RequestSpawn(ctx, a, board.SpawnInput{BriefID: s.brief, Kind: op.Target, Model: "sim", N: op.N})
	case OpClaim:
		var t *model.Task
		if t, err = s.b.ClaimTask(ctx, a, op.Task); err == nil {
			return Result{Lease: t.LeaseExpiresAt}
		}
	case OpHeartbeat:
		var hb *board.HeartbeatResult
		if hb, err = s.b.Heartbeat(ctx, a, op.Task); err == nil {
			return Result{Lease: hb.LeaseExpiresAt, Paused: hb.Paused}
		}
	case OpUpdate:
		pct, cost := op.Pct, op.Cost
		_, err = s.b.UpdateStatus(ctx, a, board.StatusInput{TaskID: op.Task, Status: model.StatusInProgress, ProgressPct: &pct, CostEUR: &cost, Note: "progress"})
	case OpBlock:
		_, err = s.b.UpdateStatus(ctx, a, board.StatusInput{TaskID: op.Task, Status: model.StatusBlocked, Note: "stuck"})
	case OpSubmit:
		_, err = s.b.Submit(ctx, a, board.SubmitInput{TaskID: op.Task, PRURL: prURL(op.Task), Summary: "done"})
	case OpRelease:
		_, err = s.b.ReleaseTask(ctx, a, op.Task, "dst")
	case OpReview:
		verdict, comments := "changes_requested", "fix it"
		if op.Approve {
			verdict, comments = "approve", "lgtm"
		}
		_, err = s.b.Review(ctx, a, op.Task, verdict, comments)
	case OpAskOwner:
		_, err = s.b.AskOwner(ctx, a, op.Task, "which way?", []string{"a", "b"})
	case OpReportTests:
		_, err = s.b.ReportTests(ctx, a, op.Task, 10, op.N, "")
	}
	return Result{Err: err}
}

func (s *BoardSystem) batch(ctx context.Context, ops []Op) Result {
	out := Result{Sub: make([]Result, len(ops))}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, op := range ops {
		wg.Go(func() {
			<-start
			out.Sub[i] = s.Do(ctx, op)
		})
	}
	close(start)
	wg.Wait()
	for _, op := range ops {
		if op.Kind == OpRotate {
			s.settleTokens(ctx, op.Target)
		}
	}
	return out
}

func (s *BoardSystem) settleTokens(ctx context.Context, id string) {
	a, err := s.b.GetAgent(ctx, id)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toks := s.tokens[id]
	for i, tok := range toks {
		if auth.Hash(tok) == a.TokenHash {
			s.tokens[id] = append(slices.Delete(slices.Clone(toks), i, i+1), tok)
			return
		}
	}
}

func (s *BoardSystem) Snapshot(ctx context.Context) (*State, error) {
	s.steps++
	all := s.last == nil || s.steps%25 == 0
	var since int64
	if n := len(s.audit); n > 0 {
		since = s.audit[n-1].ID
	}
	added, err := s.b.ListAudit(ctx, store.AuditFilter{SinceID: since, Limit: 1 << 30})
	if err != nil {
		return nil, err
	}
	s.audit = append(s.audit[:len(s.audit):len(s.audit)], added...)
	out := &State{Now: s.clock.Now(), Audit: s.audit, Tokens: map[string]int{}}
	out.Paused, _ = s.b.Paused()
	if s.last != nil {
		out.Tasks, out.Briefs, out.Agents, out.Plans, out.Spawns, out.Capacity =
			s.last.Tasks, s.last.Briefs, s.last.Agents, s.last.Plans, s.last.Spawns, s.last.Capacity
	}
	if all || len(added) > 0 || s.dirty["tasks"] {
		if out.Tasks, err = s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000}); err != nil {
			return nil, err
		}
		sort.Slice(out.Tasks, func(i, j int) bool { return idLess(out.Tasks[i].ID, out.Tasks[j].ID) })
	}
	if all || s.dirty["plans"] || s.dirty["briefs"] {
		if out.Briefs, err = s.b.ListBriefs(ctx, ""); err != nil {
			return nil, err
		}
	} else {
		out.Briefs = make([]*model.Brief, len(s.last.Briefs))
		for i, br := range s.last.Briefs {
			cp := *br
			cp.CostUsed = 0
			for _, t := range out.Tasks {
				if t.BriefID == br.ID {
					cp.CostUsed += t.CostUsed
				}
			}
			out.Briefs[i] = &cp
		}
	}
	if all || s.dirty["agents"] {
		if out.Agents, err = s.b.ListAgents(ctx, "", ""); err != nil {
			return nil, err
		}
	}
	if all || s.dirty["plans"] {
		if out.Plans, err = s.b.ListPlans(ctx, "", ""); err != nil {
			return nil, err
		}
		sort.Slice(out.Plans, func(i, j int) bool { return idLess(out.Plans[i].ID, out.Plans[j].ID) })
	}
	if all || s.dirty["spawns"] {
		if out.Spawns, err = s.b.ListSpawns(ctx, ""); err != nil {
			return nil, err
		}
		sort.Slice(out.Spawns, func(i, j int) bool { return idLess(out.Spawns[i].ID, out.Spawns[j].ID) })
	}
	if all || s.dirty["capacity"] {
		if out.Capacity, err = s.b.Capacity(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.dirty = map[string]bool{}
	for id, ts := range s.tokens {
		out.Tokens[id] = len(ts) - 1
	}
	s.mu.Unlock()
	out.Subs, out.Subscribers = s.hub.stats()
	s.last = out
	return out, nil
}

func (s *BoardSystem) Audit(ctx context.Context) ([]*model.AuditEvent, error) {
	return s.b.ListAudit(ctx, store.AuditFilter{Limit: 1 << 30})
}

func (s *BoardSystem) Close() error {
	herr := s.hub.reset()
	if err := s.st.Close(); err != nil {
		return err
	}
	return herr
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
