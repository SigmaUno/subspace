package modelcheck

import (
	"context"
	"flag"
	"os"
	"runtime"
	"testing"
	"time"
)

var (
	depth   = flag.Int("modelcheck.depth", 4, "exhaustive depth from the initial state; seeded roots use two less")
	workers = flag.Int("modelcheck.workers", runtime.GOMAXPROCS(0), "parallel board instances")
)

func newTemplate(t *testing.T) *Template {
	t.Helper()
	dir, err := os.MkdirTemp("/dev/shm", "modelcheck-")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	tp, err := NewTemplate(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

var approve = Action{Kind: ApprovePlan}

func on(k Kind, task int8) Action           { return Action{Kind: k, Task: task} }
func by(k Kind, task int8, w Worker) Action { return Action{Kind: k, Task: task, Agent: w} }

func cycle(task int8, w Worker) []Action {
	return []Action{by(Claim, task, w), on(Submit, task)}
}

func concat(parts ...[]Action) []Action {
	var out []Action
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestExhaustive(t *testing.T) {
	roots := []struct {
		name  string
		path  []Action
		depth int
	}{
		{"initial", nil, *depth},
		{"in review", concat([]Action{approve, by(Assign, 0, W1), by(Assign, 2, W2)}, cycle(0, W1)), *depth - 2},
		{"third review round", concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewChanges, 0)},
			cycle(0, W1), []Action{on(ReviewChanges, 0)}, cycle(0, W1)), *depth - 2},
		{"dependency done", concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1),
			[]Action{on(ReviewApprove, 0), on(MarkDone, 0), by(Assign, 1, W2), by(Claim, 1, W2)}), *depth - 2},
		{"brief budget nearly spent", []Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2),
			by(Assign, 0, W2), by(Claim, 0, W2)}, *depth - 2},
	}
	tp := newTemplate(t)
	for _, r := range roots {
		t.Run(r.name, func(t *testing.T) {
			start := time.Now()
			st, err := Explore(context.Background(), tp, Config{Root: r.path, Depth: r.depth, Workers: *workers})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("depth %d: %d states, %d transitions in %s", st.Depth, st.States, st.Edges, time.Since(start).Round(time.Millisecond))
		})
	}
}

func TestRegressions(t *testing.T) {
	tp := newTemplate(t)
	cases := []struct {
		name   string
		path   []Action
		task   int8
		status string
	}{
		{"leader ready on a ready task does not assign it",
			[]Action{approve, by(Assign, 0, W1), on(Cancel, 0), on(ReopenReady, 0), on(LeaderReady, 0)}, 0, Ready},
		{"reported cost counts toward the brief budget in the same call",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2), by(Assign, 0, W2), by(Claim, 0, W2), on(ReportCost, 0)}, 0, Blocked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := tp.Run(context.Background(), c.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Tasks[c.task].Status; got != c.status {
				t.Fatalf("T%d ended %s, want %s", c.task, got, c.status)
			}
		})
	}
}

func TestRegressionHeartbeatLeaseSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	sys, err := newTemplate(t).Start()
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	s, err := run(ctx, sys, []Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), {Kind: Tick}, {Kind: Heartbeat, Agent: W1}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sys.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Restore(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if s, err = step(ctx, sys, s, Action{Kind: Tick}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Tasks[2].Status != InProgress {
		t.Fatalf("T2 is %s after restart, want in_progress", s.Tasks[2].Status)
	}
}

func TestDecisions(t *testing.T) {
	tp := newTemplate(t)
	cases := []struct {
		name   string
		path   []Action
		task   int8
		status string
		code   string
	}{
		{"lease is still valid at its expiry instant",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), {Kind: Tick}}, 2, InProgress, ""},
		{"lease expires after its expiry instant",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), {Kind: Tick}, {Kind: Tick}}, 2, Assigned, ""},
		{"time budget breach blocks even when the lease expired at the same sweep",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), {Kind: Tick}, {Kind: Tick}}, 0, Blocked, ""},
		{"raising the budget unblocks to assigned",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), {Kind: Tick}, {Kind: Tick}, on(ToggleBudget, 0)}, 0, Assigned, ""},
		{"first review applies its verdict without passing through in_review",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewApprove, 0)}), 0, Approved, ""},
		{"reviewer sharing the worker provider is forbidden",
			concat([]Action{approve, by(Assign, 0, W2)}, cycle(0, W2), []Action{on(AssignReviewer, 0)}), 0, Submitted, codeForbidden},
		{"claim reports paused before ownership status and dependencies",
			[]Action{approve, {Kind: PauseAll}, by(Claim, 1, W2)}, 1, Ready, codePaused},
		{"claim checks ownership before status",
			[]Action{approve, by(Claim, 0, W1)}, 0, Ready, codeNotAssigned},
		{"claim waits for dependencies to be done not approved",
			concat([]Action{approve, by(Assign, 0, W1), by(Assign, 1, W1)}, cycle(0, W1),
				[]Action{on(ReviewApprove, 0), by(Claim, 1, W1)}), 1, Assigned, codeConflict},
		{"pause allows assignment",
			[]Action{approve, {Kind: PauseAll}, by(Assign, 0, W1)}, 0, Assigned, ""},
		{"reassign keeps changes_requested for the new assignee",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewChanges, 0), by(Reassign, 0, W2)}), 0, ChangesRequested, ""},
		{"reassign returns a blocked task to assigned",
			[]Action{approve, by(Assign, 0, W1), on(LeaderBlock, 0), by(Reassign, 0, W2)}, 0, Assigned, ""},
		{"leader may block queued tasks",
			[]Action{approve, on(LeaderBlock, 2)}, 2, Blocked, ""},
		{"agent cannot unblock its own task through update_status",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), on(BlockSelf, 0), on(ReportCost, 0)}, 0, Blocked, codeConflict},
		{"reported cost over budget blocks and reports budget_exceeded",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2)}, 2, Blocked, codeBudgetExceeded},
		{"cancelled task cost no longer counts toward the brief budget",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2), on(Cancel, 2),
				by(Assign, 0, W2), by(Claim, 0, W2), on(ReportCost, 0)}, 0, InProgress, ""},
		{"reopen to ready keeps the previous assignee",
			[]Action{approve, by(Assign, 0, W1), on(LeaderCancel, 0), on(ReopenReady, 0), on(LeaderBlock, 0), on(LeaderReady, 0)}, 0, Assigned, ""},
		{"done is terminal for cancel",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewApprove, 0), on(MarkDone, 0), on(Cancel, 0)}), 0, Done, codeConflict},
		{"submit without cross review approves directly",
			concat([]Action{approve, {Kind: ToggleCrossReview}, by(Assign, 0, W1)}, cycle(0, W1)), 0, Approved, ""},
		{"third changes_requested round needs the owner",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewChanges, 0)}, cycle(0, W1),
				[]Action{on(ReviewChanges, 0)}, cycle(0, W1), []Action{on(ReviewChanges, 0)}), 0, NeedsOwner, ""},
		{"reopening needs_owner resets review rounds",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewChanges, 0)}, cycle(0, W1),
				[]Action{on(ReviewChanges, 0)}, cycle(0, W1), []Action{on(ReviewChanges, 0), on(ReopenAssigned, 0)}), 0, Assigned, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := tp.Run(context.Background(), c.path)
			if err != nil {
				t.Fatal(err)
			}
			prev := Initial()
			for _, a := range c.path[:len(c.path)-1] {
				prev, _ = prev.Apply(a)
			}
			_, code := prev.Apply(c.path[len(c.path)-1])
			if got := s.Tasks[c.task].Status; got != c.status || code != c.code {
				t.Fatalf("T%d ended %s with code %q, want %s with %q", c.task, got, code, c.status, c.code)
			}
		})
	}
}
