package modelcheck

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	leaderID = "lead"
	repo     = "mc/repo"
	prURL    = "https://example.test/pr/1"
)

var epoch = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

type ObsTask struct {
	Status   string
	Assignee string
	Reviewer string
	Rounds   int
	Leased   bool
}

type Obs struct {
	Paused bool
	Tasks  [NumTasks]ObsTask
}

func (s State) Observe() Obs {
	o := Obs{Paused: s.Paused}
	for i, t := range s.Tasks {
		o.Tasks[i] = ObsTask{Status: t.Status, Assignee: t.Assignee.String(), Rounds: int(t.Rounds), Leased: t.Leased()}
		if t.Reviewed {
			o.Tasks[i].Reviewer = ReviewerID
		}
	}
	return o
}

type Template struct {
	dir   string
	data  []byte
	seq   atomic.Int64
	brief string
	plan  string
	tasks [NumTasks]string
}

func NewTemplate(ctx context.Context, dir string) (*Template, error) {
	tp := &Template{dir: dir}
	sys, err := tp.Start()
	if err != nil {
		return nil, err
	}
	defer sys.Close()
	if err := tp.setup(ctx, sys.b); err != nil {
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

func (tp *Template) setup(ctx context.Context, b *board.Board) error {
	agents := []board.AgentInput{
		{ID: leaderID, Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"},
		{ID: ReviewerID, Role: model.RoleReviewer, Kind: model.KindOther, Provider: ReviewerProvider},
	}
	for _, w := range Workers {
		agents = append(agents, board.AgentInput{ID: w.String(), Role: model.RoleWorker, Kind: model.KindOther, Provider: w.Provider()})
	}
	for _, a := range agents {
		if _, err := b.RegisterAgent(ctx, a); err != nil {
			return err
		}
	}
	br, err := b.CreateBrief(ctx, board.BriefInput{Title: "modelcheck", Repos: []string{repo}, MaxCostEUR: BriefMaxCost})
	if err != nil {
		return err
	}
	in := board.PlanInput{BriefID: br.ID}
	for i, spec := range Tasks {
		ti := board.TaskInput{Ref: fmt.Sprint("t", i), Title: fmt.Sprint("task ", i), Spec: "spec", Repo: repo,
			AcceptanceCriteria: []string{"works"}, Budget: budget(spec.Low)}
		for _, d := range spec.DependsOn {
			ti.DependsOn = append(ti.DependsOn, fmt.Sprint("t", d))
		}
		in.Tasks = append(in.Tasks, ti)
	}
	p, err := b.ProposePlan(ctx, model.Actor{ID: leaderID, Role: model.RoleLeader}, in)
	if err != nil {
		return err
	}
	tp.brief, tp.plan = br.ID, p.ID
	for i, t := range p.Tasks {
		tp.tasks[i] = t.ID
	}
	return nil
}

func budget(b Budget) model.Budget {
	return model.Budget{MaxMinutes: b.MaxMinutes, MaxCostEUR: float64(b.MaxCost)}
}

type System struct {
	tp   *Template
	st   *sqlite.Store
	b    *board.Board
	path string
	now  time.Time
}

type Snapshot struct {
	now    time.Time
	tasks  []*model.Task
	brief  *model.Brief
	plan   *model.Plan
	paused string
}

func (tp *Template) Start() (*System, error) {
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
	sys := &System{tp: tp, st: st, path: path, now: epoch}
	if err := sys.reload(context.Background()); err != nil {
		sys.Close()
		return nil, err
	}
	return sys, nil
}

func (s *System) reload(ctx context.Context) error {
	b, err := board.New(ctx, s.st, board.Options{
		Lease: LeaseMinutes * time.Minute,
		Now:   func() time.Time { return s.now },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s.b = b
	return err
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
		var err error
		if snap.brief, err = tx.GetBrief(s.tp.brief); err != nil {
			return err
		}
		if snap.plan, err = tx.GetPlan(s.tp.plan); err != nil {
			return err
		}
		snap.paused, _, err = tx.GetKV("paused")
		return err
	})
	return snap, err
}

func (s *System) Restore(ctx context.Context, snap *Snapshot) error {
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		for _, t := range snap.tasks {
			if err := restoreRow(func() (*model.Task, error) { return tx.GetTask(t.ID) }, tx.UpdateTask, t); err != nil {
				return err
			}
		}
		if err := restoreRow(func() (*model.Brief, error) { return tx.GetBrief(snap.brief.ID) }, tx.UpdateBrief, snap.brief); err != nil {
			return err
		}
		if err := restoreRow(func() (*model.Plan, error) { return tx.GetPlan(snap.plan.ID) }, tx.UpdatePlan, snap.plan); err != nil {
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

func (s *System) Do(ctx context.Context, from State, a Action) string {
	b, tp := s.b, s.tp
	lead := actor(leaderID, model.RoleLeader)
	rv := actor(ReviewerID, model.RoleReviewer)
	tid := tp.tasks[a.Task]
	w := actor(from.Actor(a).String(), model.RoleWorker)
	status := func(st string) *string { return &st }
	var err error
	switch a.Kind {
	case Assign:
		_, err = b.Assign(ctx, lead, tid, a.Agent.String(), "assignee")
	case Reassign:
		_, err = b.Reassign(ctx, lead, tid, a.Agent.String(), "modelcheck")
	case Claim:
		_, err = b.ClaimTask(ctx, w, tid)
	case Release:
		_, err = b.ReleaseTask(ctx, w, tid, "modelcheck")
	case Submit:
		_, err = b.Submit(ctx, w, board.SubmitInput{TaskID: tid, PRURL: prURL, Summary: "done"})
	case BlockSelf:
		_, err = b.UpdateStatus(ctx, w, board.StatusInput{TaskID: tid, Status: model.StatusBlocked, Note: "stuck"})
	case ReportCost:
		c := float64(ReportedCost)
		_, err = b.UpdateStatus(ctx, w, board.StatusInput{TaskID: tid, Status: model.StatusInProgress, CostEUR: &c})
	case ReviewApprove:
		_, err = b.Review(ctx, rv, tid, "approve", "ok")
	case ReviewChanges:
		_, err = b.Review(ctx, rv, tid, "changes_requested", "fix it")
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
	case Heartbeat:
		_, err = b.Heartbeat(ctx, actor(a.Agent.String(), model.RoleWorker), "")
	case Tick:
		s.now = s.now.Add(TickMinutes * time.Minute)
		b.Flush(ctx)
		err = b.Sweep(ctx)
	case PauseAll:
		err = b.PauseAll(ctx, "modelcheck")
	case ResumeAll:
		err = b.ResumeAll(ctx)
	case ApprovePlan:
		_, err = b.ApprovePlan(ctx, tp.plan, "")
	case ToggleCrossReview:
		cr := from.NoCrossReview
		_, err = b.UpdateBrief(ctx, tp.brief, board.BriefPatch{CrossReview: &cr})
	}
	if err == nil {
		return ""
	}
	return model.AsError(err).Code
}

func (s *System) Observe(ctx context.Context) (Obs, error) {
	p, _ := s.b.Paused()
	o := Obs{Paused: p}
	ts, err := s.b.ListTasks(ctx, board.TaskQuery{BriefID: s.tp.brief})
	if err != nil {
		return o, err
	}
	idx := map[string]int{}
	for i, id := range s.tp.tasks {
		idx[id] = i
	}
	for _, t := range ts {
		i, ok := idx[t.ID]
		if !ok {
			return o, fmt.Errorf("unexpected task %s", t.ID)
		}
		o.Tasks[i] = ObsTask{Status: t.Status, Rounds: t.ReviewRounds, Leased: t.LeaseExpiresAt != nil}
		if t.Assignee != nil {
			o.Tasks[i].Assignee = *t.Assignee
		}
		if t.Reviewer != nil {
			o.Tasks[i].Reviewer = *t.Reviewer
		}
	}
	return o, nil
}
