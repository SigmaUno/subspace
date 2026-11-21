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
	depth   = flag.Int("modelcheck.depth", 3, "exhaustive depth from the initial state; seeded roots use two less")
	deep    = flag.Bool("modelcheck.deep", false, "explore two levels deeper from every root")
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

var (
	approve = Action{Kind: ApprovePlan}
	tick    = Action{Kind: Tick}
)

func lead(k Kind, task int8) Leader {
	if !k.leader() {
		return 0
	}
	return Tasks[task].Brief.Leader()
}

func on(k Kind, task int8) Action { return Action{Kind: k, Task: task, Lead: lead(k, task)} }
func by(k Kind, task int8, w Worker) Action {
	return Action{Kind: k, Task: task, Agent: w, Lead: lead(k, task)}
}
func foreign(a Action) Action {
	a.Lead = L1 + L2 - a.Lead
	return a
}
func global(k Kind) Action               { return Action{Kind: k} }
func agent(k Kind, w Worker) Action      { return Action{Kind: k, Agent: w} }
func cycle(task int8, w Worker) []Action { return []Action{by(Claim, task, w), on(Submit, task)} }
func expire(task int8, w Worker) []Action {
	return []Action{by(Claim, task, w), tick, tick}
}

func concat(parts ...[]Action) []Action {
	var out []Action
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestExhaustive(t *testing.T) {
	extra := 0
	if *deep {
		extra = 2
	}
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
		{"quota agent busy in the other brief", []Action{approve, by(Assign, 3, W2), by(Claim, 3, W2), by(Assign, 2, W1)}, *depth - 2},
		{"gated brief without cross review", []Action{approve, global(ToggleGates), global(ToggleCrossReview),
			by(Assign, 0, W1), by(Claim, 0, W1), by(Assign, 2, W2), by(Claim, 2, W2)}, *depth - 2},
		{"brief closed with work in flight", []Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), by(Assign, 2, W2),
			global(CloseBrief)}, *depth - 2},
		{"lease expired twice", concat([]Action{approve, by(Assign, 2, W1)}, expire(2, W1), expire(2, W1),
			[]Action{by(Claim, 2, W1)}), *depth - 2},
	}
	tp := newTemplate(t)
	for _, r := range roots {
		t.Run(r.name, func(t *testing.T) {
			start := time.Now()
			st, err := Explore(context.Background(), tp, Config{Root: r.path, Depth: r.depth + extra, Workers: *workers})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("depth %d: %d states, %d transitions in %s", st.Depth, st.States, st.Edges, time.Since(start).Round(time.Millisecond))
		})
	}
}

type outcome struct {
	name   string
	path   []Action
	task   int8
	status string
	code   string
}

func checkOutcomes(t *testing.T, run func(context.Context, []Action) (State, error), flap bool, cases []outcome) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := run(context.Background(), c.path)
			if err != nil {
				t.Fatal(err)
			}
			prev := Initial(flap)
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

func TestRegressions(t *testing.T) {
	tp := newTemplate(t)
	checkOutcomes(t, tp.Run, false, []outcome{
		{"leader ready on a ready task does not assign it",
			[]Action{approve, by(Assign, 0, W1), on(Cancel, 0), on(ReopenReady, 0), on(LeaderReady, 0)}, 0, Ready, ""},
		{"reported cost counts toward the brief budget in the same call",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2), by(Assign, 0, W2), by(Claim, 0, W2), on(ReportCost, 0)}, 0, Blocked, codeBudgetExceeded},
		{"leader assign counts the quota over tasks in briefs it does not lead",
			[]Action{approve, by(Assign, 3, W2), by(Assign, 0, W2)}, 0, Ready, codeQuotaExceeded},
	})
}

func TestRegressionHeartbeatLeaseSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	sys, err := newTemplate(t).Start()
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	s, err := run(ctx, sys, []Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), tick, agent(Heartbeat, W1)})
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
	if s, err = step(ctx, sys, s, tick, nil); err != nil {
		t.Fatal(err)
	}
	if s.Tasks[2].Status != InProgress {
		t.Fatalf("T2 is %s after restart, want in_progress", s.Tasks[2].Status)
	}
}

func TestDecisions(t *testing.T) {
	tp := newTemplate(t)
	threeRounds := concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewChanges, 0)}, cycle(0, W1),
		[]Action{on(ReviewChanges, 0)}, cycle(0, W1), []Action{on(ReviewChanges, 0)})
	poisoned := concat([]Action{approve, by(Assign, 2, W1)}, expire(2, W1), expire(2, W1), expire(2, W1))
	checkOutcomes(t, tp.Run, false, []outcome{
		{"lease is still valid at its expiry instant",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), tick}, 2, InProgress, ""},
		{"lease expires after its expiry instant",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), tick, tick}, 2, Assigned, ""},
		{"time budget breach blocks even when the lease expired at the same sweep",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), tick, tick}, 0, Blocked, ""},
		{"raising the budget unblocks to assigned",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), tick, tick, on(ToggleBudget, 0)}, 0, Assigned, ""},
		{"first review applies its verdict without passing through in_review",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewApprove, 0)}), 0, Approved, ""},
		{"reviewer sharing the worker provider is forbidden",
			concat([]Action{approve, by(Assign, 0, W2)}, cycle(0, W2), []Action{on(AssignReviewer, 0)}), 0, Submitted, codeForbidden},
		{"claim reports paused before ownership status and dependencies",
			[]Action{approve, global(PauseAll), by(Claim, 1, W2)}, 1, Ready, codePaused},
		{"claim checks ownership before status",
			[]Action{approve, by(Claim, 0, W1)}, 0, Ready, codeNotAssigned},
		{"claim waits for dependencies to be done not approved",
			concat([]Action{approve, by(Assign, 0, W1), by(Assign, 1, W1)}, cycle(0, W1),
				[]Action{on(ReviewApprove, 0), by(Claim, 1, W1)}), 1, Assigned, codeConflict},
		{"pause allows assignment",
			[]Action{approve, global(PauseAll), by(Assign, 0, W1)}, 0, Assigned, ""},
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
		{"cancelled task cost still counts toward the brief budget",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2), on(Cancel, 2),
				by(Assign, 0, W2), by(Claim, 0, W2), on(ReportCost, 0)}, 0, Blocked, codeBudgetExceeded},
		{"reopen to ready keeps the previous assignee",
			[]Action{approve, by(Assign, 0, W1), on(LeaderCancel, 0), on(ReopenReady, 0), on(LeaderBlock, 0), on(LeaderReady, 0)}, 0, Assigned, ""},
		{"done is terminal for cancel",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewApprove, 0), on(MarkDone, 0), on(Cancel, 0)}), 0, Done, codeConflict},
		{"submit without cross review approves directly",
			concat([]Action{approve, global(ToggleCrossReview), by(Assign, 0, W1)}, cycle(0, W1)), 0, Approved, ""},
		{"third changes_requested round needs the owner", threeRounds, 0, NeedsOwner, ""},
		{"reopening needs_owner resets review rounds", concat(threeRounds, []Action{on(ReopenAssigned, 0)}), 0, Assigned, ""},

		{"approval with failing tests is held in review",
			concat([]Action{approve, global(ToggleGates), by(Assign, 0, W1), by(Claim, 0, W1), on(TestsFailed, 0), on(Submit, 0),
				on(ReviewApprove, 0)}), 0, InReview, codeGatesFailed},
		{"approval without a test report is held in review",
			concat([]Action{approve, global(ToggleGates), by(Assign, 0, W1)}, cycle(0, W1), []Action{on(ReviewApprove, 0)}), 0, InReview, codeGatesFailed},
		{"approval with passing tests clears the gates",
			concat([]Action{approve, global(ToggleGates), by(Assign, 0, W1), by(Claim, 0, W1), on(TestsPassed, 0), on(Submit, 0),
				on(ReviewApprove, 0)}), 0, Approved, ""},
		{"disabling the gates releases a held approval",
			concat([]Action{approve, global(ToggleGates), by(Assign, 0, W1)}, cycle(0, W1),
				[]Action{on(ReviewApprove, 0), global(ToggleGates), on(ReviewApprove, 0)}), 0, Approved, ""},
		{"gated submit without cross review stays submitted and reports gates_failed",
			concat([]Action{approve, global(ToggleGates), global(ToggleCrossReview), by(Assign, 0, W1)}, cycle(0, W1)), 0, Submitted, codeGatesFailed},
		{"held submission is promoted once its tests pass",
			concat([]Action{approve, global(ToggleGates), global(ToggleCrossReview), by(Assign, 0, W1), by(Claim, 0, W1),
				on(TestsPassed, 0), on(Submit, 0), on(PromoteGated, 0)}), 0, Approved, ""},
		{"promotion leaves a cross reviewed submission for the reviewer",
			concat([]Action{approve, global(ToggleGates), by(Assign, 0, W1), by(Claim, 0, W1),
				on(TestsPassed, 0), on(Submit, 0), on(PromoteGated, 0)}), 0, Submitted, ""},
		{"gates of one brief do not hold the other",
			[]Action{global(ToggleGates), by(Assign, 3, W1), by(Claim, 3, W1), on(Submit, 3), on(ReviewApprove, 3)}, 3, Approved, ""},

		{"assigning a reopened task back to its previous assignee skips the quota",
			[]Action{approve, by(Assign, 0, W2), on(Cancel, 0), on(ReopenReady, 0), by(Assign, 3, W2), by(Assign, 0, W2)}, 0, Assigned, ""},
		{"claim enforces the quota on an agent holding two tasks",
			[]Action{approve, by(Assign, 0, W2), on(Cancel, 0), on(ReopenReady, 0), by(Assign, 3, W2), by(Assign, 0, W2),
				by(Claim, 3, W2), by(Claim, 0, W2)}, 0, Assigned, codeQuotaExceeded},
		{"quota does not apply to reassigning the agent its own task",
			[]Action{approve, by(Assign, 3, W2), by(Assign, 0, W1), by(Claim, 3, W2), by(Reassign, 0, W1)}, 0, Assigned, ""},
		{"quota leaves agents without a quota alone",
			[]Action{approve, by(Assign, 0, W1), by(Assign, 2, W1), by(Assign, 3, W1)}, 3, Assigned, ""},

		{"closing a brief cancels queued work and blocks work in flight",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), global(CloseBrief)}, 0, Blocked, ""},
		{"closing a brief cancels assigned tasks",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), by(Assign, 2, W2), global(CloseBrief)}, 2, Cancelled, ""},
		{"closing a brief leaves submitted work alone",
			concat([]Action{approve, by(Assign, 0, W1)}, cycle(0, W1), []Action{global(CloseBrief)}), 0, Submitted, ""},
		{"closing a brief leaves the other brief alone",
			[]Action{approve, by(Assign, 3, W1), global(CloseBrief)}, 3, Assigned, ""},
		{"claim on a closed brief conflicts",
			[]Action{approve, by(Assign, 0, W1), global(CloseBrief), on(ReopenAssigned, 0), by(Claim, 0, W1)}, 0, Assigned, codeConflict},
		{"leader cannot unblock a task blocked by closing its brief",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), global(CloseBrief), global(ReopenBrief), on(LeaderReady, 0)}, 0, Blocked, codeForbidden},
		{"reopening a brief keeps cancelled tasks cancelled",
			[]Action{approve, by(Assign, 0, W1), global(CloseBrief), global(ReopenBrief)}, 0, Cancelled, ""},
		{"assigning a reopened task of a closed brief is allowed",
			[]Action{approve, global(CloseBrief), on(ReopenReady, 2), by(Assign, 2, W1)}, 2, Assigned, ""},

		{"announcements do not move tasks",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), global(Announce)}, 0, InProgress, ""},

		{"leader cannot see tasks of a brief it does not lead",
			[]Action{approve, foreign(by(Assign, 0, W1))}, 0, Ready, codeNotFound},
		{"second leader assigns in its own brief",
			[]Action{by(Assign, 3, W1)}, 3, Assigned, ""},
		{"leader cannot block tasks of another brief",
			[]Action{foreign(on(LeaderBlock, 3))}, 3, Ready, codeNotFound},
		{"leader cannot set reviewers on another brief",
			concat([]Action{by(Assign, 3, W1)}, cycle(3, W1), []Action{foreign(on(AssignReviewer, 3))}), 3, Submitted, codeNotFound},

		{"third lease expiry blocks the task as poison", poisoned, 2, Blocked, ""},
		{"leader cannot unblock a poison task", concat(poisoned, []Action{on(LeaderReady, 2)}), 2, Blocked, codeForbidden},
		{"leader cannot reassign a poison task", concat(poisoned, []Action{by(Reassign, 2, W2)}), 2, Blocked, codeForbidden},
		{"owner reopen resets the lease expiry count",
			concat(poisoned, []Action{on(ReopenAssigned, 2)}, expire(2, W1)), 2, Assigned, ""},
		{"reassign keeps the lease expiry count",
			concat([]Action{approve, by(Assign, 2, W1)}, expire(2, W1), expire(2, W1), []Action{by(Reassign, 2, W2)}, expire(2, W2)), 2, Blocked, ""},
		{"leader unblock resets the lease expiry count",
			concat([]Action{approve, by(Assign, 2, W1)}, expire(2, W1), expire(2, W1), []Action{on(LeaderBlock, 2), on(LeaderReady, 2)},
				expire(2, W1)), 2, Assigned, ""},
		{"leader cannot unblock a budget block",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(ReportCost, 2), on(LeaderReady, 2)}, 2, Blocked, codeForbidden},
		{"leader unblocks a task the agent blocked",
			[]Action{approve, by(Assign, 2, W1), by(Claim, 2, W1), on(BlockSelf, 2), on(LeaderReady, 2)}, 2, Assigned, ""},
		{"owner unblocks a budget block by reopening",
			[]Action{approve, by(Assign, 0, W1), by(Claim, 0, W1), tick, tick, on(ReopenAssigned, 0)}, 0, Assigned, ""},
	})
}

func TestFlapping(t *testing.T) {
	tp := newTemplate(t)
	twoAtOnce := []Action{approve, by(Assign, 2, W1), by(Assign, 3, W1), by(Claim, 2, W1), by(Claim, 3, W1), tick, tick}
	paused := concat(twoAtOnce, expire(2, W1))
	checkOutcomes(t, tp.RunFlapping, true, []outcome{
		{"two expiries at once do not pause the agent", twoAtOnce, 3, Assigned, ""},
		{"third expiry within the window pauses the agent", paused, 2, Assigned, ""},
		{"paused agent cannot claim", concat(paused, []Action{by(Claim, 3, W1)}), 3, Assigned, codeAgentPaused},
		{"paused agent cannot be assigned more work", concat(paused, []Action{by(Assign, 0, W1)}), 0, Ready, codeConflict},
		{"resumed agent claims again", concat(paused, []Action{agent(ResumeAgent, W1), by(Claim, 3, W1)}), 3, InProgress, ""},
		{"expiries outside the window do not count",
			concat([]Action{approve, by(Assign, 2, W1), by(Assign, 3, W1)}, expire(2, W1), []Action{tick, tick}, expire(3, W1),
				[]Action{tick, tick}, expire(2, W1), []Action{by(Claim, 3, W1)}), 3, InProgress, ""},
		{"pausing a flapping agent releases its other leases",
			concat([]Action{approve, by(Assign, 2, W1), by(Assign, 3, W1), by(Claim, 2, W1), by(Claim, 3, W1), tick, agent(Heartbeat, W1),
				on(Release, 2), tick, tick}, expire(2, W1), expire(2, W1)), 3, Assigned, ""},
	})
}
