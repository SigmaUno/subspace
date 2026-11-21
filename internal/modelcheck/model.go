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
	codeNotFound       = "not_found"
	codePaused         = "paused"
	codeAgentPaused    = "agent_paused"
	codeForbidden      = "forbidden"
	codeBadRequest     = "bad_request"
	codeBudgetExceeded = "budget_exceeded"
	codeQuotaExceeded  = "quota_exceeded"
	codeGatesFailed    = "gates_failed"
)

const (
	NumTasks        = 4
	LeaseMinutes    = 10
	TickMinutes     = 10
	MaxReviewRounds = 3
	BriefMaxCost    = 2
	ReportedCost    = 1
	PoisonExpiries  = 3
	FlapExpiries    = 3
	FlapWindow      = 30
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

func (w Worker) MaxConcurrent() int {
	if w == W2 {
		return 1
	}
	return 0
}

func (w Worker) bit() uint8 {
	switch w {
	case W1:
		return 1 << 1
	case W2:
		return 1 << 2
	}
	return 0
}

const (
	ReviewerID       = "rv"
	ReviewerProvider = "openai"
)

type Leader int8

const (
	L1 Leader = iota + 1
	L2
)

func (l Leader) String() string {
	if l == L2 {
		return "lead2"
	}
	return "lead"
}

type Brief int8

const (
	BriefA Brief = iota
	BriefB
)

func (b Brief) Leader() Leader {
	if b == BriefB {
		return L2
	}
	return L1
}

type Budget struct {
	MaxMinutes int
	MaxCost    int
}

type TaskSpec struct {
	Brief     Brief
	DependsOn []int
	Low, High Budget
}

var Tasks = [NumTasks]TaskSpec{
	{Low: Budget{MaxMinutes: 20}, High: Budget{MaxMinutes: 60}},
	{DependsOn: []int{0}},
	{Low: Budget{MaxCost: 1}, High: Budget{MaxCost: 5}},
	{Brief: BriefB},
}

type Block int8

const (
	BlockNone Block = iota
	BlockLeader
	BlockAgent
	BlockBudget
	BlockPoison
	BlockClosed
)

func (r Block) leaderMayUnblock() bool { return r == BlockLeader || r == BlockAgent }

type Tests int8

const (
	TestsNone Tests = iota
	TestsPass
	TestsFail
)

type Task struct {
	Status     string
	Assignee   Worker
	Reviewed   bool
	Rounds     int8
	LeaseUntil int
	ClaimedAt  int
	Minutes    int
	Cost       int
	HighBudget bool
	Block      Block
	Expiries   int8
	Tests      Tests
	Claims     int8
	Seen       uint8
	Reassigned bool
}

func (t Task) Leased() bool { return t.Status == InProgress }

func (t Task) handoff() (bool, uint8) {
	switch t.Status {
	case Assigned, ChangesRequested, InProgress:
	default:
		return false, 0
	}
	if t.Assignee == Nobody {
		return false, 0
	}
	prev := t.Seen &^ t.Assignee.bit()
	claims := t.Claims
	if t.Status == InProgress {
		claims--
	}
	if claims <= 0 && prev == 0 && !t.Reassigned && t.Rounds == 0 {
		return false, 0
	}
	return true, prev
}

type State struct {
	Now           int
	Paused        bool
	NoCrossReview bool
	PlanApproved  bool
	Gates         bool
	Closed        bool
	Flap          bool
	AgentPaused   [3]bool
	Flaps         [3][FlapExpiries]int
	Tasks         [NumTasks]Task
}

func Initial(flap bool) State {
	s := State{Flap: flap}
	for i := range s.Tasks {
		s.Tasks[i].Status = Backlog
		if Tasks[i].Brief == BriefB {
			s.Tasks[i].Status = Ready
		}
	}
	return s
}

func (s State) budget(i int) Budget {
	if s.Tasks[i].HighBudget {
		return Tasks[i].High
	}
	return Tasks[i].Low
}

func (s State) briefCost(b Brief) int {
	c := 0
	for i, t := range s.Tasks {
		if Tasks[i].Brief == b {
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
	return Tasks[i].Brief != BriefA || s.briefCost(BriefA) < BriefMaxCost
}

func (s State) holding(w Worker, statuses ...string) int {
	n := 0
	for _, t := range s.Tasks {
		if t.Assignee != w {
			continue
		}
		for _, st := range statuses {
			if t.Status == st {
				n++
			}
		}
	}
	return n
}

func (s State) assignQuotaHit(w Worker) bool {
	q := w.MaxConcurrent()
	return q > 0 && s.holding(w, InProgress, Assigned, ChangesRequested) >= q
}

func (s State) claimQuotaHit(w Worker) bool {
	q := w.MaxConcurrent()
	return q > 0 && s.holding(w, InProgress) >= q
}

func (s *State) endLease(i int) {
	t := &s.Tasks[i]
	if t.Status == InProgress {
		t.Minutes += s.Now - t.ClaimedAt
	}
	t.LeaseUntil, t.ClaimedAt = 0, 0
}

func (s *State) block(i int, reason Block) {
	s.endLease(i)
	s.Tasks[i].Status = Blocked
	s.Tasks[i].Block = reason
}

func (s *State) unblockTo(i int) string {
	if s.Tasks[i].Assignee != Nobody {
		return Assigned
	}
	return Ready
}

func (s *State) recordExpiry(w Worker) {
	if !s.Flap || w == Nobody {
		return
	}
	var kept [FlapExpiries]int
	n := 0
	for _, at := range s.Flaps[w] {
		if at > 0 && at-1 > s.Now-FlapWindow && n < FlapExpiries-1 {
			kept[n] = at
			n++
		}
	}
	kept[n] = s.Now + 1
	n++
	s.Flaps[w] = kept
	if n >= FlapExpiries {
		s.pauseAgent(w)
	}
}

func (s *State) pauseAgent(w Worker) {
	s.AgentPaused[w] = true
	s.Flaps[w] = [FlapExpiries]int{}
	for j := range s.Tasks {
		if s.Tasks[j].Status == InProgress && s.Tasks[j].Assignee == w {
			s.endLease(j)
			s.Tasks[j].Status = Assigned
		}
	}
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
	TestsPassed
	TestsFailed
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
	PromoteGated
	Heartbeat
	ResumeAgent
	Tick
	PauseAll
	ResumeAll
	ApprovePlan
	ToggleCrossReview
	ToggleGates
	CloseBrief
	ReopenBrief
	Announce
)

var kindNames = [...]string{"assign", "reassign", "claim", "release", "submit", "block_self", "report_cost",
	"tests_passed", "tests_failed", "review_approve", "review_changes", "assign_reviewer", "mark_done", "cancel",
	"reopen_ready", "reopen_assigned", "leader_block", "leader_ready", "leader_cancel", "toggle_budget", "promote_gated",
	"heartbeat", "resume_agent", "tick", "pause_all", "resume_all", "approve_plan", "toggle_cross_review",
	"toggle_gates", "close_brief", "reopen_brief", "announce"}

func (k Kind) leader() bool {
	switch k {
	case Assign, Reassign, AssignReviewer, LeaderBlock, LeaderReady, LeaderCancel:
		return true
	}
	return false
}

func (k Kind) idempotent() bool {
	switch k {
	case Claim, Submit, ReviewApprove, ReviewChanges:
		return true
	}
	return false
}

type Action struct {
	Kind  Kind
	Task  int8
	Agent Worker
	Lead  Leader
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
	if a.Kind.leader() {
		b.WriteString(" by " + a.Lead.String())
	}
	return b.String()
}

func Alphabet() []Action {
	var out []Action
	for i := int8(0); i < NumTasks; i++ {
		spec := Tasks[i]
		own, foreign := spec.Brief.Leader(), BriefB.Leader()
		if spec.Brief == BriefB {
			foreign = BriefA.Leader()
		}
		for _, w := range Workers {
			out = append(out, Action{Kind: Assign, Task: i, Agent: w, Lead: own}, Action{Kind: Claim, Task: i, Agent: w})
			if spec.Brief == BriefA {
				out = append(out, Action{Kind: Reassign, Task: i, Agent: w, Lead: own})
			}
		}
		kinds := []Kind{Release, Submit, Cancel, ReopenAssigned}
		leads := []Kind{LeaderBlock, LeaderReady}
		if spec.Brief == BriefA {
			kinds = []Kind{Release, Submit, BlockSelf, ReportCost, TestsPassed, TestsFailed, ReviewApprove, ReviewChanges,
				MarkDone, Cancel, ReopenReady, ReopenAssigned, PromoteGated}
			leads = []Kind{AssignReviewer, LeaderBlock, LeaderReady, LeaderCancel}
		}
		for _, k := range kinds {
			out = append(out, Action{Kind: k, Task: i})
		}
		for _, k := range leads {
			out = append(out, Action{Kind: k, Task: i, Lead: own})
		}
		if spec.Low != spec.High {
			out = append(out, Action{Kind: ToggleBudget, Task: i})
		}
		out = append(out, Action{Kind: Assign, Task: i, Agent: W1, Lead: foreign}, Action{Kind: Reassign, Task: i, Agent: W2, Lead: foreign})
		for _, k := range []Kind{AssignReviewer, LeaderBlock, LeaderReady, LeaderCancel} {
			out = append(out, Action{Kind: k, Task: i, Lead: foreign})
		}
	}
	for _, w := range Workers {
		out = append(out, Action{Kind: Heartbeat, Agent: w}, Action{Kind: ResumeAgent, Agent: w})
	}
	for _, k := range []Kind{Tick, PauseAll, ResumeAll, ApprovePlan, ToggleCrossReview, ToggleGates, CloseBrief, ReopenBrief, Announce} {
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
	Task
	LeaseLeft int
	Elapsed   int
}

type Key struct {
	Paused, NoCrossReview, PlanApproved, Gates, Closed bool
	AgentPaused                                        [3]bool
	FlapAges                                           [3][FlapExpiries]int
	Tasks                                              [NumTasks]taskKey
}

func (s State) Key() Key {
	k := Key{Paused: s.Paused, NoCrossReview: s.NoCrossReview, PlanApproved: s.PlanApproved, Gates: s.Gates,
		Closed: s.Closed, AgentPaused: s.AgentPaused}
	for w, fs := range s.Flaps {
		for j, at := range fs {
			if at > 0 {
				k.FlapAges[w][j] = s.Now - at + 2
			}
		}
	}
	for i, t := range s.Tasks {
		tk := taskKey{Task: t}
		tk.LeaseUntil, tk.ClaimedAt = 0, 0
		if t.Status != Blocked {
			tk.Block = BlockNone
		}
		timed := Tasks[i].Low.MaxMinutes > 0 || Tasks[i].High.MaxMinutes > 0
		if t.Status == InProgress {
			tk.LeaseLeft = t.LeaseUntil - s.Now
			if timed {
				tk.Elapsed = s.Now - t.ClaimedAt
			}
		}
		if !timed {
			tk.Minutes = 0
		}
		k.Tasks[i] = tk
	}
	return k
}

func (s *State) apply(a Action) string {
	i := int(a.Task)
	t := &s.Tasks[i]
	spec := Tasks[i]
	if a.Kind.leader() && a.Lead != spec.Brief.Leader() {
		return codeNotFound
	}
	switch a.Kind {
	case Assign, Reassign:
		if s.AgentPaused[a.Agent] {
			return codeConflict
		}
		if a.Kind == Assign {
			if t.Status != Ready {
				return codeConflict
			}
		} else {
			switch t.Status {
			case Ready, Assigned, InProgress, Blocked, ChangesRequested:
			default:
				return codeConflict
			}
			if t.Status == Blocked && !t.Block.leaderMayUnblock() {
				return codeForbidden
			}
		}
		if a.Agent != t.Assignee && s.assignQuotaHit(a.Agent) {
			return codeQuotaExceeded
		}
		if a.Kind == Reassign {
			t.Reassigned = true
			if t.Status == ChangesRequested {
				t.Assignee = a.Agent
				t.Seen |= a.Agent.bit()
				return ""
			}
		}
		s.endLease(i)
		t.Status, t.Assignee, t.Block = Assigned, a.Agent, BlockNone
	case Claim:
		if s.AgentPaused[a.Agent] {
			return codeAgentPaused
		}
		if s.Paused {
			return codePaused
		}
		if t.Assignee != a.Agent {
			return codeNotAssigned
		}
		if t.Status != Assigned && t.Status != ChangesRequested {
			return codeConflict
		}
		for _, d := range spec.DependsOn {
			if s.Tasks[d].Status != Done {
				return codeConflict
			}
		}
		if spec.Brief == BriefA && s.Closed {
			return codeConflict
		}
		if !s.withinBudget(i) {
			return codeBudgetExceeded
		}
		if s.claimQuotaHit(a.Agent) {
			return codeQuotaExceeded
		}
		t.Status, t.ClaimedAt, t.LeaseUntil = InProgress, s.Now, s.Now+LeaseMinutes
		t.Claims = min(t.Claims+1, 2)
		t.Seen |= a.Agent.bit()
	case Release, Submit, BlockSelf, ReportCost, TestsPassed, TestsFailed:
		w := s.Actor(a)
		if a.Kind == Submit && s.AgentPaused[w] {
			return codeAgentPaused
		}
		if t.Assignee != w || t.Assignee == Nobody {
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
			if spec.Brief == BriefA && s.NoCrossReview {
				if s.Gates && t.Tests != TestsPass {
					return codeGatesFailed
				}
				t.Status = Approved
			}
		case BlockSelf:
			s.block(i, BlockAgent)
		case ReportCost:
			t.Cost = max(t.Cost, ReportedCost)
			if !s.withinBudget(i) {
				s.block(i, BlockBudget)
				return codeBudgetExceeded
			}
		case TestsPassed:
			t.Tests = TestsPass
		case TestsFailed:
			t.Tests = TestsFail
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
			if spec.Brief == BriefA && s.Gates && t.Tests != TestsPass {
				t.Status = InReview
				return codeGatesFailed
			}
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
		if t.Status == NeedsOwner || t.Rounds >= MaxReviewRounds {
			t.Rounds = 0
		}
		t.Status, t.Block, t.Expiries = to, BlockNone, 0
	case LeaderBlock:
		switch t.Status {
		case Ready, Assigned, InProgress:
			s.block(i, BlockLeader)
		default:
			return codeConflict
		}
	case LeaderReady:
		switch t.Status {
		case Blocked:
			if !t.Block.leaderMayUnblock() {
				return codeForbidden
			}
			t.Status = s.unblockTo(i)
		case Ready:
		case Backlog:
			return codeForbidden
		default:
			return codeConflict
		}
		t.Block, t.Expiries = BlockNone, 0
	case ToggleBudget:
		t.HighBudget = !t.HighBudget
		if t.Status == Blocked && t.Block == BlockBudget && s.withinBudget(i) {
			t.Status, t.Block = s.unblockTo(i), BlockNone
		}
	case PromoteGated:
		if t.Status == Submitted && spec.Brief == BriefA && s.NoCrossReview && s.Gates && t.Tests == TestsPass {
			t.Status = Approved
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
	case ResumeAgent:
		s.AgentPaused[a.Agent] = false
	case Tick:
		s.tick()
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
			if Tasks[j].Brief == BriefA && s.Tasks[j].Status == Backlog {
				s.Tasks[j].Status = Ready
			}
		}
	case ToggleCrossReview:
		s.NoCrossReview = !s.NoCrossReview
	case ToggleGates:
		s.Gates = !s.Gates
	case CloseBrief:
		if s.Closed {
			return ""
		}
		s.Closed = true
		for j := range s.Tasks {
			if Tasks[j].Brief != BriefA {
				continue
			}
			switch s.Tasks[j].Status {
			case Backlog, Ready, Assigned:
				s.Tasks[j].Status = Cancelled
			case InProgress:
				s.block(j, BlockClosed)
			}
		}
	case ReopenBrief:
		s.Closed = false
	case Announce:
	}
	return ""
}

func (s *State) tick() {
	s.Now += TickMinutes
	for j := range s.Tasks {
		u := &s.Tasks[j]
		if u.Status != InProgress {
			continue
		}
		if b := s.budget(j); b.MaxMinutes > 0 && u.Minutes+s.Now-u.ClaimedAt >= b.MaxMinutes {
			s.block(j, BlockBudget)
			continue
		}
		if u.LeaseUntil >= s.Now {
			continue
		}
		u.Expiries++
		if u.Expiries >= PoisonExpiries {
			s.block(j, BlockPoison)
		} else {
			s.endLease(j)
			u.Status = Assigned
		}
		s.recordExpiry(u.Assignee)
	}
}
