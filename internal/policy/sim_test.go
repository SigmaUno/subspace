package policy_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/policy"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var simPolicies = []policy.Document{
	{Name: "security to anthropic", Enabled: true, Rules: []policy.Rule{{ID: "route",
		When: policy.When{Actions: []string{policy.ActAssign, policy.ActClaim}, Match: `"security" in task.labels && request.as != "reviewer"`},
		Then: policy.Then{Effect: policy.EffectRoute, Provider: "anthropic"}}}},
	{Name: "deploy needs owner", Enabled: true, Rules: []policy.Rule{{ID: "deploy",
		When: policy.When{Actions: []string{policy.ActAssign}, Match: `overlaps(task.paths, "deploy/**") && request.as == "assignee"`},
		Then: policy.Then{Effect: policy.EffectApproval, Timeout: "6h"}}}},
	{Name: "caps", Enabled: true, Timezone: "Europe/Berlin", Rules: []policy.Rule{
		{ID: "component", When: policy.When{Actions: []string{policy.ActAssign, policy.ActClaim}, Match: `task.component == "api" && cost.component_used >= 3`},
			Then: policy.Then{Effect: policy.EffectDeny, Message: "api spent its budget"}},
		{ID: "parallel", When: policy.When{Actions: []string{policy.ActClaim}},
			Then: policy.Then{Effect: policy.EffectMaxParallel, Max: 3, Per: policy.PerProvider}},
		{ID: "weekend", When: policy.When{Actions: []string{policy.ActAssign}, Match: `time.weekday in ["sat", "sun"] && task.priority < 2`},
			Then: policy.Then{Effect: policy.EffectDeny}},
		{ID: "spend", When: policy.When{Actions: []string{policy.ActSubmit}, Match: `cost.task_used > 1`},
			Then: policy.Then{Effect: policy.EffectWarn, Message: "expensive task"}},
		{ID: "no-opencode", When: policy.When{Actions: []string{policy.ActSpawn}, Match: `spawn.kind == "opencode"`},
			Then: policy.Then{Effect: policy.EffectDeny}},
		{ID: "secrets", When: policy.When{Actions: []string{policy.ActCreateTask, policy.ActUpdateTask}, Match: `overlaps(task.paths, "secrets/**")`},
			Then: policy.Then{Effect: policy.EffectApproval}},
		{ID: "security-review", When: policy.When{Actions: []string{policy.ActReview, policy.ActMergeReady}, Match: `"security" in task.labels && actor.provider == "anthropic"`},
			Then: policy.Then{Effect: policy.EffectDeny}},
	}},
}

type sim struct {
	t      *testing.T
	ctx    context.Context
	st     *sqlite.Store
	b      *board.Board
	svc    *policy.Service
	rng    *rand.Rand
	now    time.Time
	lead   model.Actor
	agents []*model.Agent
	brief  string
	trace  []string
}

func newSim(t *testing.T, seed uint64) *sim {
	t.Helper()
	st, err := sqlite.Open(t.TempDir() + "/sim.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &sim{t: t, ctx: context.Background(), st: st, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		now: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC), lead: model.Actor{ID: "rex", Role: model.RoleLeader}}
	clock := func() time.Time { return s.now }
	if s.b, err = board.New(s.ctx, st, board.Options{Now: clock, Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	s.svc = policy.New(policy.Options{Store: st, Now: clock})
	s.svc.SetReplayer(policy.ActAssign, func(ctx context.Context, h *policy.Hold) (string, error) {
		_, err := s.b.Assign(ctx, model.Actor{ID: h.ActorID, Role: h.ActorRole}, h.TaskID, h.Args["agent_id"], h.Args["as"])
		return "assigned", err
	})
	s.b.SetPolicy(s.svc)
	for _, a := range []board.AgentInput{
		{ID: "rex", Role: model.RoleLeader, Kind: model.KindOther},
		{ID: "ant-1", Role: model.RoleWorker, Kind: model.KindClaudeCode},
		{ID: "ant-2", Role: model.RoleWorker, Kind: model.KindClaudeCode},
		{ID: "oai-1", Role: model.RoleWorker, Kind: model.KindCodex},
		{ID: "ds-1", Role: model.RoleWorker, Kind: model.KindOpenCode},
		{ID: "tst-1", Role: model.RoleTester, Kind: model.KindClaudeCode},
		{ID: "tst-2", Role: model.RoleTester, Kind: model.KindCodex},
		{ID: "rv-oai", Role: model.RoleReviewer, Kind: model.KindCodex},
		{ID: "rv-ant", Role: model.RoleReviewer, Kind: model.KindClaudeCode},
	} {
		r, err := s.b.RegisterAgent(s.ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		s.agents = append(s.agents, r.Agent)
	}
	cross := true
	br, err := s.b.CreateBrief(s.ctx, board.BriefInput{Title: "sim", Repos: []string{"o/r"}, MaxCostEUR: 1000, CrossReview: &cross})
	if err != nil {
		t.Fatal(err)
	}
	s.brief = br.ID
	var in []board.TaskInput
	for i := range 14 {
		in = append(in, s.randomTask(i))
	}
	p, err := s.b.ProposePlan(s.ctx, s.lead, board.PlanInput{BriefID: br.ID, Tasks: in})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.ApprovePlan(s.ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, d := range simPolicies {
		if _, err := s.svc.Create(s.ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *sim) randomTask(i int) board.TaskInput {
	in := board.TaskInput{Title: fmt.Sprintf("task %d", i), Spec: "s", Repo: "o/r", Priority: s.rng.IntN(4)}
	switch s.rng.IntN(6) {
	case 0:
		in.Title = "[security] " + in.Title
	case 1:
		in.Paths = []string{"deploy/svc-" + fmt.Sprint(i)}
	case 2:
		in.ComponentID = "api"
	case 3:
		in.Paths = []string{"secrets/vault"}
	}
	if s.rng.IntN(4) == 0 {
		in.Role = model.RoleTester
	}
	return in
}

func pick[T any](r *rand.Rand, xs []T) (T, bool) {
	var zero T
	if len(xs) == 0 {
		return zero, false
	}
	return xs[r.IntN(len(xs))], true
}

func (s *sim) actor(a *model.Agent) model.Actor {
	return model.Actor{ID: a.ID, Role: a.Role, Kind: a.Kind}
}

func (s *sim) tasks(statuses ...string) []*model.Task {
	ts, err := s.b.ListTasks(s.ctx, board.TaskQuery{BriefID: s.brief, Limit: 10000})
	if err != nil {
		s.t.Fatal(err)
	}
	var out []*model.Task
	for _, t := range ts {
		if len(statuses) == 0 || slices.Contains(statuses, t.Status) {
			out = append(out, t)
		}
	}
	return out
}

func (s *sim) agentsOf(role string) []*model.Agent {
	var out []*model.Agent
	for _, a := range s.agents {
		if a.Role == role {
			out = append(out, a)
		}
	}
	return out
}

func (s *sim) step(i int) {
	op := s.rng.IntN(13)
	var (
		name string
		err  error
	)
	pre := s.snapshot()
	switch op {
	case 0, 1:
		name = "assign"
		t, ok := pick(s.rng, s.tasks(model.StatusReady))
		a, _ := pick(s.rng, s.agentsOf(model.RoleWorker))
		if ok && t.Role == model.RoleTester {
			a, _ = pick(s.rng, s.agentsOf(model.RoleTester))
		}
		if ok {
			name += " " + t.ID + " " + a.ID
			_, err = s.b.Assign(s.ctx, s.lead, t.ID, a.ID, "")
		}
	case 2:
		name = "assign-reviewer"
		t, ok := pick(s.rng, s.tasks(model.StatusSubmitted))
		a, _ := pick(s.rng, s.agentsOf(model.RoleReviewer))
		if ok {
			name += " " + t.ID + " " + a.ID
			_, err = s.b.Assign(s.ctx, s.lead, t.ID, a.ID, "reviewer")
		}
	case 3, 4:
		name = "claim"
		t, ok := pick(s.rng, s.tasks(model.StatusAssigned, model.StatusChangesRequested))
		if ok {
			name += " " + t.ID
			_, err = s.b.ClaimTask(s.ctx, s.actor(s.agent(*t.Assignee)), t.ID)
		}
	case 5:
		name = "cost"
		t, ok := pick(s.rng, s.tasks(model.StatusInProgress))
		if ok {
			c := t.CostUsed + float64(s.rng.IntN(3))
			name += fmt.Sprintf(" %s %g", t.ID, c)
			_, err = s.b.UpdateStatus(s.ctx, s.actor(s.agent(*t.Assignee)), board.StatusInput{TaskID: t.ID, Status: model.StatusInProgress, CostEUR: &c})
		}
	case 6:
		name = "submit"
		t, ok := pick(s.rng, s.tasks(model.StatusInProgress))
		if ok {
			name += " " + t.ID
			_, err = s.b.Submit(s.ctx, s.actor(s.agent(*t.Assignee)), board.SubmitInput{TaskID: t.ID, PRURL: "https://git.test/o/r/pulls/1"})
		}
	case 7:
		name = "review"
		t, ok := pick(s.rng, s.tasks(model.StatusSubmitted, model.StatusInReview))
		a, _ := pick(s.rng, s.agentsOf(model.RoleReviewer))
		if ok {
			verdict := []string{"approve", "changes_requested"}[s.rng.IntN(2)]
			name += " " + t.ID + " " + a.ID + " " + verdict
			_, err = s.b.Review(s.ctx, s.actor(a), t.ID, verdict, "c")
		}
	case 8:
		name = "create"
		in := s.randomTask(100 + i)
		in.BriefID = s.brief
		name += " " + in.Title
		_, err = s.b.CreateTask(s.ctx, s.lead, in)
	case 9:
		name = "spawn"
		kind := []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}[s.rng.IntN(3)]
		name += " " + kind
		_, err = s.b.RequestSpawn(s.ctx, s.lead, board.SpawnInput{BriefID: s.brief, Kind: kind, Model: "m", N: 1})
	case 10:
		name = "decide"
		hs, herr := s.svc.Holds(s.ctx, policy.HoldPending, 100)
		if herr != nil {
			s.t.Fatal(herr)
		}
		if h, ok := pick(s.rng, hs); ok {
			if s.rng.IntN(3) == 0 {
				name += " reject " + h.ID
				_, err = s.svc.Reject(s.ctx, h.ID, "no")
			} else {
				name += " approve " + h.ID
				_, err = s.svc.Approve(s.ctx, h.ID, "ok")
			}
		}
	case 11:
		d := time.Duration(1+s.rng.IntN(20)) * time.Hour
		name = "advance " + d.String()
		s.now = s.now.Add(d)
	case 12:
		name = "sweep"
		_, err = s.svc.Sweep(s.ctx)
	}
	code := "ok"
	if err != nil {
		code = model.AsError(err).Code
	}
	s.trace = append(s.trace, fmt.Sprintf("%d %s -> %s", i, name, code))
	if code == board.CodePolicyDenied || code == board.CodePolicyHold {
		if post := s.snapshot(); post != pre {
			s.t.Fatalf("step %d %s returned %s but changed state:\n%s", i, name, code, diff(pre, post))
		}
	}
	s.checkInvariants(i, name)
}

func (s *sim) agent(id string) *model.Agent {
	for _, a := range s.agents {
		if a.ID == id {
			return a
		}
	}
	s.t.Fatalf("unknown agent %s", id)
	return nil
}

func (s *sim) checkInvariants(i int, name string) {
	consumed := map[string]bool{}
	hs, err := s.svc.Holds(s.ctx, policy.HoldConsumed, 1000)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, h := range hs {
		if h.Action == policy.ActAssign {
			consumed[h.TaskID] = true
		}
	}
	for _, t := range s.tasks() {
		if t.Assignee == nil {
			continue
		}
		a := s.agent(*t.Assignee)
		if slices.Contains(policy.Labels(t.Title, t.Spec), "security") && a.Provider != "anthropic" {
			s.t.Fatalf("step %d %s: security task %s held by %s (%s)", i, name, t.ID, a.ID, a.Provider)
		}
		if len(t.Paths) > 0 && strings.HasPrefix(t.Paths[0], "deploy/") && !consumed[t.ID] {
			s.t.Fatalf("step %d %s: deploy task %s assigned without an approved hold", i, name, t.ID)
		}
	}
}

var snapshotTables = []string{"briefs", "plans", "tasks", "agents", "messages", "questions", "spawns", "kv",
	"seq WHERE name NOT IN ('PD','PH')", "audit WHERE tool <> 'policy_hold'"}

func (s *sim) snapshot() string {
	var sb strings.Builder
	err := s.st.View(s.ctx, func(tx store.Tx) error {
		q := tx.(interface {
			Query(string, ...any) (*sql.Rows, error)
		})
		for _, table := range snapshotTables {
			rows, err := q.Query(`SELECT * FROM ` + table)
			if err != nil {
				return err
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					_ = rows.Close()
					return err
				}
				fmt.Fprintf(&sb, "%s %v\n", strings.Fields(table)[0], vals)
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return sb.String()
}

func diff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for _, l := range bl {
		if !slices.Contains(al, l) {
			out = append(out, "+ "+l)
		}
	}
	for _, l := range al {
		if !slices.Contains(bl, l) {
			out = append(out, "- "+l)
		}
	}
	return strings.Join(out, "\n")
}

func runSim(t *testing.T, seed uint64, steps int) (string, map[string]int) {
	s := newSim(t, seed)
	for i := range steps {
		s.step(i)
	}
	codes := map[string]int{}
	for _, l := range s.trace {
		codes[l[strings.LastIndex(l, " ")+1:]]++
	}
	h := sha256.Sum256([]byte(strings.Join(s.trace, "\n")))
	return hex.EncodeToString(h[:]), codes
}

func TestSimulationDeniedActionsNeverChangeState(t *testing.T) {
	total := map[string]int{}
	var first string
	for _, seed := range []uint64{1, 2, 3} {
		trace, codes := runSim(t, seed, 200)
		if seed == 1 {
			first = trace
		}
		for k, v := range codes {
			total[k] += v
		}
	}
	if again, _ := runSim(t, 1, 200); again != first {
		t.Fatal("seed 1 is not deterministic")
	}
	for _, code := range []string{"ok", board.CodePolicyDenied, board.CodePolicyHold} {
		if total[code] == 0 {
			t.Fatalf("simulation never produced %s: %v", code, total)
		}
	}
}
