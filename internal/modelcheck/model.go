package modelcheck

import (
	"fmt"
	"strings"
)

const (
	Backlog          = "backlog"
	Ready            = "ready"
	Assigned         = "assigned"
	InProgress       = "in_progress"
	Submitted        = "submitted"
	InReview         = "in_review"
	ChangesRequested = "changes_requested"
	Approved         = "approved"
	Done             = "done"
	Blocked          = "blocked"
	NeedsOwner       = "needs_owner"
	Cancelled        = "cancelled"
)

const (
	codeConflict       = "conflict"
	codeNotAssigned    = "not_assigned"
	codePaused         = "paused"
	codeForbidden      = "forbidden"
	codeBadRequest     = "bad_request"
	codeBudgetExceeded = "budget_exceeded"
)

const (
	NumTasks        = 3
	LeaseMinutes    = 10
	TickMinutes     = 10
	MaxReviewRounds = 3
	BriefMaxCost    = 2
	ReportedCost    = 1
)

type Worker int8

const (
	Nobody Worker = iota
	W1
	W2
)

var Workers = []Worker{W1, W2}

func (w Worker) String() string {
	switch w {
	case W1:
		return "w1"
	case W2:
		return "w2"
	}
	return ""
}

func (w Worker) Provider() string {
	switch w {
	case W1:
		return "anthropic"
	case W2:
		return "openai"
	}
	return ""
}

const (
	ReviewerID       = "rv"
	ReviewerProvider = "openai"
)

type Budget struct {
	MaxMinutes int
	MaxCost    int
}

type TaskSpec struct {
	DependsOn []int
	Low, High Budget
}

var Tasks = [NumTasks]TaskSpec{
	{Low: Budget{MaxMinutes: 20}, High: Budget{MaxMinutes: 60}},
	{DependsOn: []int{0}},
	{Low: Budget{MaxCost: 1}, High: Budget{MaxCost: 5}},
}

type Task struct {
	Status      string
	Assignee    Worker
	Reviewed    bool
	Rounds      int8
	LeaseUntil  int
	ClaimedAt   int
	Minutes     int
	Cost        int
	HighBudget  bool
	BudgetBlock bool
}

func (t Task) Leased() bool { return t.Status == InProgress }

type State struct {
	Now           int
	Paused        bool
	NoCrossReview bool
	PlanApproved  bool
	Tasks         [NumTasks]Task
}

func Initial() State {
	var s State
	for i := range s.Tasks {
		s.Tasks[i].Status = Backlog
	}
	return s
}

func (s State) budget(i int) Budget {
	if s.Tasks[i].HighBudget {
		return Tasks[i].High
	}
	return Tasks[i].Low
}

func (s State) briefCost() int {
	c := 0
	for _, t := range s.Tasks {
		if t.Status != Cancelled {
			c += t.Cost
		}
	}
	return c
}

func (s State) withinBudget(i int) bool {
	t, b := s.Tasks[i], s.budget(i)
	if b.MaxCost > 0 && t.Cost >= b.MaxCost {
		return false
	}
	if b.MaxMinutes > 0 && t.Minutes >= b.MaxMinutes {
		return false
	}
	return s.briefCost() < BriefMaxCost
}

func (s *State) endLease(i int) {
	t := &s.Tasks[i]
	if t.Status == InProgress {
		t.Minutes += s.Now - t.ClaimedAt
	}
	t.LeaseUntil, t.ClaimedAt = 0, 0
}

func (s *State) block(i int, budget bool) {
	s.endLease(i)
	s.Tasks[i].Status = Blocked
	s.Tasks[i].BudgetBlock = budget
}

func (s *State) unblockTo(i int) string {
	if s.Tasks[i].Assignee != Nobody {
		return Assigned
	}
	return Ready
}

type Kind int8

const (
	Assign Kind = iota
	Reassign
	Claim
	Release
	Submit
	BlockSelf
	ReportCost
	ReviewApprove
	ReviewChanges
	AssignReviewer
	MarkDone
	Cancel
	ReopenReady
	ReopenAssigned
	LeaderBlock
	LeaderReady
	LeaderCancel
	ToggleBudget
	Heartbeat
	Tick
	PauseAll
	ResumeAll
	ApprovePlan
	ToggleCrossReview
)

var kindNames = [...]string{"assign", "reassign", "claim", "release", "submit", "block_self", "report_cost",
	"review_approve", "review_changes", "assign_reviewer", "mark_done", "cancel", "reopen_ready", "reopen_assigned",
	"leader_block", "leader_ready", "leader_cancel", "toggle_budget", "heartbeat", "tick", "pause_all", "resume_all",
	"approve_plan", "toggle_cross_review"}

type Action struct {
	Kind  Kind
	Task  int8
	Agent Worker
}

func (a Action) String() string {
	var b strings.Builder
	b.WriteString(kindNames[a.Kind])
	if a.Kind < Heartbeat {
		fmt.Fprintf(&b, " T%d", a.Task)
	}
	if a.Agent != Nobody {
		b.WriteString(" " + a.Agent.String())
	}
	return b.String()
}

func Alphabet() []Action {
	var out []Action
	for i := int8(0); i < NumTasks; i++ {
		for _, w := range Workers {
			out = append(out, Action{Kind: Assign, Task: i, Agent: w}, Action{Kind: Reassign, Task: i, Agent: w}, Action{Kind: Claim, Task: i, Agent: w})
		}
		for _, k := range []Kind{Release, Submit, BlockSelf, ReportCost, ReviewApprove, ReviewChanges, AssignReviewer,
			MarkDone, Cancel, ReopenReady, ReopenAssigned, LeaderBlock, LeaderReady, LeaderCancel} {
			out = append(out, Action{Kind: k, Task: i})
		}
		if Tasks[i].Low != Tasks[i].High {
			out = append(out, Action{Kind: ToggleBudget, Task: i})
		}
	}
	for _, w := range Workers {
		out = append(out, Action{Kind: Heartbeat, Agent: w})
	}
	for _, k := range []Kind{Tick, PauseAll, ResumeAll, ApprovePlan, ToggleCrossReview} {
		out = append(out, Action{Kind: k})
	}
	return out
}

func (s State) Actor(a Action) Worker {
	if a.Agent != Nobody {
		return a.Agent
	}
	if w := s.Tasks[a.Task].Assignee; w != Nobody {
		return w
	}
	return W1
}

func terminal(st string) bool { return st == Done || st == Cancelled }

func (s State) Apply(a Action) (State, string) {
	code := s.apply(a)
	return s, code
}

type taskKey struct {
	Status      string
	Assignee    Worker
	Reviewed    bool
	Rounds      int8
	LeaseLeft   int
	Elapsed     int
	Minutes     int
	Cost        int
	HighBudget  bool
	BudgetBlock bool
}

type Key struct {
	Paused, NoCrossReview, PlanApproved bool
	Tasks                               [NumTasks]taskKey
}

func (s State) Key() Key {
	k := Key{Paused: s.Paused, NoCrossReview: s.NoCrossReview, PlanApproved: s.PlanApproved}
	for i, t := range s.Tasks {
		tk := taskKey{Status: t.Status, Assignee: t.Assignee, Reviewed: t.Reviewed, Rounds: t.Rounds, Cost: t.Cost,
			HighBudget: t.HighBudget, BudgetBlock: t.Status == Blocked && t.BudgetBlock}
		timed := Tasks[i].Low.MaxMinutes > 0 || Tasks[i].High.MaxMinutes > 0
		if t.Status == InProgress {
			tk.LeaseLeft = t.LeaseUntil - s.Now
			if timed {
				tk.Elapsed = s.Now - t.ClaimedAt
			}
		}
		if timed {
			tk.Minutes = t.Minutes
		}
		k.Tasks[i] = tk
	}
	return k
}

func (s *State) apply(a Action) string {
	i := int(a.Task)
	t := &s.Tasks[i]
	switch a.Kind {
	case Assign:
		if t.Status != Ready {
			return codeConflict
		}
		t.Status, t.Assignee = Assigned, a.Agent
	case Reassign:
		switch t.Status {
		case Ready, Assigned, InProgress, Blocked:
			s.endLease(i)
			t.Status = Assigned
		case ChangesRequested:
		default:
			return codeConflict
		}
		t.Assignee = a.Agent
	case Claim:
		if s.Paused {
			return codePaused
		}
		if t.Assignee != a.Agent {
			return codeNotAssigned
		}
		if t.Status != Assigned && t.Status != ChangesRequested {
			return codeConflict
		}
		for _, d := range Tasks[i].DependsOn {
			if s.Tasks[d].Status != Done {
				return codeConflict
			}
		}
		if !s.withinBudget(i) {
			return codeBudgetExceeded
		}
		t.Status, t.ClaimedAt, t.LeaseUntil = InProgress, s.Now, s.Now+LeaseMinutes
	case Release, Submit, BlockSelf, ReportCost:
		if t.Assignee != s.Actor(a) || t.Assignee == Nobody {
			return codeNotAssigned
		}
		if t.Status != InProgress {
			return codeConflict
		}
		switch a.Kind {
		case Release:
			s.endLease(i)
			t.Status = Assigned
		case Submit:
			s.endLease(i)
			t.Status = Submitted
			if s.NoCrossReview {
				t.Status = Approved
			}
		case BlockSelf:
			s.block(i, false)
		case ReportCost:
			t.Cost = max(t.Cost, ReportedCost)
			if !s.withinBudget(i) {
				s.block(i, true)
				return codeBudgetExceeded
			}
		}
	case ReviewApprove, ReviewChanges, AssignReviewer:
		if t.Status != Submitted && t.Status != InReview {
			return codeConflict
		}
		if t.Assignee.Provider() == ReviewerProvider {
			return codeForbidden
		}
		t.Reviewed = true
		switch a.Kind {
		case AssignReviewer:
			t.Status = InReview
		case ReviewApprove:
			t.Status = Approved
		case ReviewChanges:
			t.Rounds++
			t.Status = ChangesRequested
			if t.Rounds >= MaxReviewRounds {
				t.Status = NeedsOwner
			}
		}
	case MarkDone:
		if t.Status != Approved && t.Status != NeedsOwner {
			return codeConflict
		}
		t.Status = Done
	case Cancel, LeaderCancel:
		if terminal(t.Status) {
			return codeConflict
		}
		s.endLease(i)
		t.Status = Cancelled
	case ReopenReady, ReopenAssigned:
		if t.Status != NeedsOwner && t.Status != Blocked && t.Status != Cancelled {
			return codeConflict
		}
		to := Ready
		if a.Kind == ReopenAssigned {
			if t.Assignee == Nobody {
				return codeBadRequest
			}
			to = Assigned
		}
		if t.Status == NeedsOwner {
			t.Rounds = 0
		}
		t.Status = to
	case LeaderBlock:
		switch t.Status {
		case Ready, Assigned, InProgress:
			s.block(i, false)
		default:
			return codeConflict
		}
	case LeaderReady:
		switch t.Status {
		case Blocked:
			t.Status = s.unblockTo(i)
		case Ready:
		case Backlog:
			return codeForbidden
		default:
			return codeConflict
		}
	case ToggleBudget:
		t.HighBudget = !t.HighBudget
		if t.Status == Blocked && t.BudgetBlock && s.withinBudget(i) {
			t.Status = s.unblockTo(i)
		}
	case Heartbeat:
		if s.Paused {
			return ""
		}
		for j := range s.Tasks {
			if s.Tasks[j].Status == InProgress && s.Tasks[j].Assignee == a.Agent {
				s.Tasks[j].LeaseUntil = s.Now + LeaseMinutes
			}
		}
	case Tick:
		s.Now += TickMinutes
		for j := range s.Tasks {
			u := &s.Tasks[j]
			if u.Status != InProgress {
				continue
			}
			if b := s.budget(j); b.MaxMinutes > 0 && u.Minutes+s.Now-u.ClaimedAt >= b.MaxMinutes {
				s.block(j, true)
			} else if u.LeaseUntil < s.Now {
				s.endLease(j)
				u.Status = Assigned
			}
		}
	case PauseAll:
		s.Paused = true
		for j := range s.Tasks {
			if s.Tasks[j].Status == InProgress {
				s.endLease(j)
				s.Tasks[j].Status = Assigned
			}
		}
	case ResumeAll:
		s.Paused = false
	case ApprovePlan:
		if s.PlanApproved {
			return codeConflict
		}
		s.PlanApproved = true
		for j := range s.Tasks {
			if s.Tasks[j].Status == Backlog {
				s.Tasks[j].Status = Ready
			}
		}
	case ToggleCrossReview:
		s.NoCrossReview = !s.NoCrossReview
	}
	return ""
}
