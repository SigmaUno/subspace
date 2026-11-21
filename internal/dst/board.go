package dst

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type BoardSystem struct {
	st     *sqlite.Store
	b      *board.Board
	clock  *Clock
	roles  map[string]string
	tokens map[string]string
}

func NewBoardSystem(dir string, clock *Clock, lease time.Duration) (System, error) {
	return newBoardSystem(dir, clock, board.Options{Lease: lease})
}

func newBoardSystem(dir string, clock *Clock, o board.Options) (*BoardSystem, error) {
	st, err := sqlite.Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		return nil, err
	}
	o.Now = clock.Now
	o.Log = discard
	b, err := board.New(context.Background(), st, o)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	return &BoardSystem{st: st, b: b, clock: clock, roles: map[string]string{}, tokens: map[string]string{}}, nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func (s *BoardSystem) actor(id string) model.Actor {
	return model.Actor{ID: id, Role: s.roles[id]}
}

func prURL(taskID string) string {
	return "https://git.example/" + simRepo + "/pull/" + strings.TrimPrefix(taskID, "T-")
}

const simRepo = "sim/repo"

func (s *BoardSystem) Setup(ctx context.Context, w World) (Setup, error) {
	var out Setup
	br, err := s.b.CreateBrief(ctx, board.BriefInput{Title: "dst", Body: "simulated brief", Repos: []string{simRepo},
		MaxCostEUR: w.BriefCost, MaxParallel: w.MaxParallel, AllowedProviders: w.AllowedProviders})
	if err != nil {
		return out, err
	}
	agents := append([]AgentSpec{{ID: w.Leader, Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"}}, w.Agents...)
	for _, a := range agents {
		r, err := s.b.RegisterAgent(ctx, board.AgentInput{ID: a.ID, Role: a.Role, Kind: a.Kind, Provider: a.Provider, Model: "sim"})
		if err != nil {
			return out, err
		}
		s.roles[a.ID] = a.Role
		s.tokens[a.ID] = r.Connect.Token
	}
	in := board.PlanInput{BriefID: br.ID}
	for _, t := range w.Tasks {
		in.Tasks = append(in.Tasks, board.TaskInput{Ref: t.Ref, Title: "task " + t.Ref, Spec: "spec " + t.Ref, Repo: simRepo,
			Role: t.Role, DependsOn: t.DependsOn, Budget: t.Budget})
	}
	p, err := s.b.ProposePlan(ctx, s.actor(w.Leader), in)
	if err != nil {
		return out, err
	}
	out.PlanID, out.BriefID = p.ID, br.ID
	for _, t := range p.Tasks {
		out.TaskIDs = append(out.TaskIDs, t.ID)
	}
	return out, nil
}

func (s *BoardSystem) Do(ctx context.Context, op Op) Result {
	a := s.actor(op.Actor)
	var err error
	switch op.Kind {
	case OpAdvance:
		s.clock.Advance(op.Dur)
	case OpSweep:
		err = s.b.Sweep(ctx)
	case OpFlush:
		s.b.Flush(ctx)
	case OpApprove:
		_, err = s.b.ApprovePlan(ctx, op.Target, "")
	case OpPauseAll:
		err = s.b.PauseAll(ctx, "dst")
	case OpResumeAll:
		err = s.b.ResumeAll(ctx)
	case OpMarkDone:
		_, err = s.b.MarkDone(ctx, op.Task, "merged")
	case OpReopen:
		_, err = s.b.Reopen(ctx, op.Task, model.StatusAssigned, "")
	case OpAnswer:
		_, err = s.b.Answer(ctx, op.Target, "yes")
	case OpAssign:
		_, err = s.b.Assign(ctx, a, op.Task, op.Target, "assignee")
	case OpAssignReviewer:
		_, err = s.b.Assign(ctx, a, op.Task, op.Target, "reviewer")
	case OpReassign:
		_, err = s.b.Reassign(ctx, a, op.Task, op.Target, "dst")
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
	case OpCancel:
		_, err = s.b.Cancel(ctx, op.Task, "dst")
	case OpReportTests:
		_, err = s.b.ReportTests(ctx, a, op.Task, 10, op.N, "")
	}
	return Result{Err: err}
}

func (s *BoardSystem) Snapshot(ctx context.Context) (*State, error) {
	out := &State{Now: s.clock.Now()}
	out.Paused, _ = s.b.Paused()
	var err error
	if out.Tasks, err = s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000}); err != nil {
		return nil, err
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return idLess(out.Tasks[i].ID, out.Tasks[j].ID) })
	if out.Briefs, err = s.b.ListBriefs(ctx, ""); err != nil {
		return nil, err
	}
	if out.Audit, err = s.b.ListAudit(ctx, store.AuditFilter{Limit: 1 << 30}); err != nil {
		return nil, err
	}
	if out.Agents, err = s.b.ListAgents(ctx, "", ""); err != nil {
		return nil, err
	}
	for _, a := range out.Agents {
		s.roles[a.ID] = a.Role
	}
	return out, nil
}

func (s *BoardSystem) Close() error { return s.st.Close() }

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
