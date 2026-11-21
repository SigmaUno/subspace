package replay

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	MaxPoints      = 500
	MaxTransitions = 5000
	DefaultEvery   = 256
)

type Task struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	Assignee       *string    `json:"assignee"`
	Reviewer       *string    `json:"reviewer"`
	ProgressPct    int        `json:"progress_pct"`
	CostUsed       float64    `json:"cost_used"`
	ChangedAuditID int64      `json:"changed_audit_id"`
	ChangedAt      *time.Time `json:"changed_at"`
}

type Agent struct {
	ID          string     `json:"id"`
	Role        string     `json:"role"`
	Assigned    []string   `json:"assigned"`
	Reviewing   []string   `json:"reviewing"`
	Events      int        `json:"events"`
	LastEventAt *time.Time `json:"last_event_at"`
}

type Snapshot struct {
	BriefID string            `json:"brief_id"`
	AuditID int64             `json:"audit_id"`
	At      *time.Time        `json:"at"`
	Events  int               `json:"events"`
	Total   int               `json:"total"`
	Counts  map[string]int    `json:"counts"`
	Columns map[string][]Task `json:"columns"`
	Agents  []Agent           `json:"agents"`
}

type Point struct {
	AuditID int64          `json:"audit_id"`
	At      time.Time      `json:"at"`
	Counts  map[string]int `json:"counts"`
}

type KeyEvent struct {
	AuditID int64     `json:"audit_id"`
	At      time.Time `json:"at"`
	Tool    string    `json:"tool"`
	AgentID string    `json:"agent_id"`
	TaskID  string    `json:"task_id"`
	From    string    `json:"from"`
	To      string    `json:"to"`

	Transcript string `json:"transcript,omitempty"`
}

type Timeline struct {
	BriefID   string     `json:"brief_id"`
	Events    int        `json:"events"`
	Start     *time.Time `json:"start"`
	End       *time.Time `json:"end"`
	Points    []Point    `json:"points"`
	KeyEvents []KeyEvent `json:"key_events"`
}

type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

type Transition struct {
	AuditID int64             `json:"audit_id"`
	At      time.Time         `json:"at"`
	TaskID  string            `json:"task_id"`
	Tool    string            `json:"tool"`
	AgentID string            `json:"agent_id"`
	Changes map[string]Change `json:"changes"`
}

type Diff struct {
	BriefID     string       `json:"brief_id"`
	From        int64        `json:"from"`
	To          int64        `json:"to"`
	Transitions []Transition `json:"transitions"`
	Truncated   bool         `json:"truncated"`
}

type taskState struct {
	status   string
	assignee string
	reviewer string
	progress int
	cost     float64
	auditID  int64
	at       time.Time
}

type agentState struct {
	role   string
	events int
	last   time.Time
}

type state struct {
	n      int
	tasks  map[string]taskState
	agents map[string]agentState
}

func newState() *state {
	return &state{tasks: map[string]taskState{}, agents: map[string]agentState{}}
}

func (s *state) clone() *state {
	return &state{n: s.n, tasks: maps.Clone(s.tasks), agents: maps.Clone(s.agents)}
}

type History struct {
	briefID     string
	every       int
	tasks       map[string]*model.Task
	roles       map[string]string
	events      []*model.AuditEvent
	marks       []time.Time
	high        time.Time
	checkpoints []*state
	head        *state
	scanned     int64
}

func NewHistory(briefID string, every int) *History {
	if every <= 0 {
		every = DefaultEvery
	}
	return &History{briefID: briefID, every: every, tasks: map[string]*model.Task{}, roles: map[string]string{},
		checkpoints: []*state{newState()}, head: newState()}
}

func (h *History) Scanned() int64 { return h.scanned }

func (h *History) Len() int { return len(h.events) }

func (h *History) Update(tasks []*model.Task, roles map[string]string) {
	for _, t := range tasks {
		if t.BriefID == h.briefID {
			h.tasks[t.ID] = t
		}
	}
	maps.Copy(h.roles, roles)
}

func (h *History) Append(events []*model.AuditEvent) {
	for _, e := range events {
		if e.ID <= h.scanned {
			continue
		}
		h.scanned = e.ID
		if e.TS.After(h.high) {
			h.high = e.TS
		}
		if !h.relevant(e) {
			continue
		}
		h.events = append(h.events, e)
		h.marks = append(h.marks, h.high)
		h.apply(h.head, e, nil)
		if h.head.n%h.every == 0 {
			h.checkpoints = append(h.checkpoints, h.head.clone())
		}
	}
}

func (h *History) member(id string) bool {
	_, ok := h.tasks[id]
	return ok
}

func (h *History) relevant(e *model.AuditEvent) bool {
	if e.TaskID != nil {
		return h.member(*e.TaskID)
	}
	switch e.Tool {
	case "propose_plan", "approve_plan", "reject_plan":
		return slices.ContainsFunc(append(ids(e.After["task_ids"]), ids(e.After["superseded"])...), h.member)
	}
	return false
}

func ids(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func (h *History) apply(st *state, e *model.AuditEvent, emit func(Transition)) {
	st.n++
	if e.AgentID != "" && e.AgentID != model.Owner.ID && e.Role != "system" {
		a := st.agents[e.AgentID]
		a.role, a.events, a.last = e.Role, a.events+1, e.TS
		st.agents[e.AgentID] = a
	}
	if e.TaskID != nil {
		h.applyTask(st, e, *e.TaskID, emit)
		return
	}
	switch e.Tool {
	case "propose_plan":
		for _, id := range ids(e.After["superseded"]) {
			if cur, ok := st.tasks[id]; ok && h.member(id) {
				next := cur
				next.status = model.StatusCancelled
				h.set(st, e, id, cur, true, next, emit)
			}
		}
		for _, id := range ids(e.After["task_ids"]) {
			if _, ok := st.tasks[id]; !ok && h.member(id) {
				h.set(st, e, id, taskState{}, false, taskState{status: model.StatusBacklog}, emit)
			}
		}
	case "approve_plan", "reject_plan":
		to := model.StatusReady
		if e.Tool == "reject_plan" {
			to = model.StatusCancelled
		}
		for _, id := range ids(e.After["task_ids"]) {
			if cur, ok := st.tasks[id]; ok && h.member(id) {
				next := cur
				next.status = to
				h.set(st, e, id, cur, true, next, emit)
			}
		}
	}
}

func (h *History) applyTask(st *state, e *model.AuditEvent, id string, emit func(Transition)) {
	cur, existed := st.tasks[id]
	next := cur
	if !existed {
		next = taskState{status: h.initialStatus(e, id)}
	}
	fields, ok := e.After["state"].(map[string]any)
	if !ok {
		fields = e.After
	}
	if s := str(fields["status"]); s != "" {
		next.status = s
	}
	if v, ok := fields["assignee"]; ok {
		next.assignee = str(v)
	}
	if v, ok := fields["reviewer"]; ok {
		next.reviewer = str(v)
	}
	if n, ok := num(fields["progress_pct"]); ok {
		next.progress = int(math.Round(n))
	}
	if n, ok := num(fields["cost_used"]); ok {
		next.cost = n
	}
	h.set(st, e, id, cur, existed, next, emit)
}

func (h *History) initialStatus(e *model.AuditEvent, id string) string {
	switch e.Tool {
	case "create_task", "add_plan_task":
		if s := str(e.After["status"]); s != "" {
			return s
		}
		return model.StatusBacklog
	}
	if s := str(e.Before["status"]); s != "" {
		return s
	}
	return h.tasks[id].Status
}

func optional(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h *History) set(st *state, e *model.AuditEvent, id string, cur taskState, existed bool, next taskState, emit func(Transition)) {
	changes := map[string]Change{}
	if !existed || cur.status != next.status {
		var from any
		if existed {
			from = cur.status
		}
		changes["status"] = Change{from, next.status}
	}
	if cur.assignee != next.assignee {
		changes["assignee"] = Change{optional(cur.assignee), optional(next.assignee)}
	}
	if cur.reviewer != next.reviewer {
		changes["reviewer"] = Change{optional(cur.reviewer), optional(next.reviewer)}
	}
	if cur.progress != next.progress {
		changes["progress_pct"] = Change{cur.progress, next.progress}
	}
	if cur.cost != next.cost {
		changes["cost_used"] = Change{cur.cost, next.cost}
	}
	if len(changes) == 0 {
		return
	}
	next.auditID, next.at = e.ID, e.TS
	st.tasks[id] = next
	if emit != nil {
		emit(Transition{AuditID: e.ID, At: e.TS.UTC(), TaskID: id, Tool: e.Tool, AgentID: e.AgentID, Changes: changes})
	}
}

func (h *History) stateAt(n int) *state {
	if n >= len(h.events) {
		return h.head
	}
	st := h.checkpoints[n/h.every].clone()
	for i := st.n; i < n; i++ {
		h.apply(st, h.events[i], nil)
	}
	return st
}

func (h *History) Resolve(at string) (int, error) {
	if at == "" {
		return len(h.events), nil
	}
	if strings.TrimLeft(at, "0123456789") == "" {
		id, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			return 0, model.BadRequest("invalid audit id %q", at)
		}
		return sort.Search(len(h.events), func(i int) bool { return h.events[i].ID > id }), nil
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return 0, model.BadRequest("at must be an RFC 3339 time or an audit id: %q", at)
	}
	return sort.Search(len(h.marks), func(i int) bool { return h.marks[i].After(t) }), nil
}

func (h *History) auditID(n int) (int64, *time.Time) {
	if n <= 0 || n > len(h.events) {
		return 0, nil
	}
	e := h.events[n-1]
	at := e.TS.UTC()
	return e.ID, &at
}

type view struct {
	id string
	ts taskState
}

func (h *History) present(st *state, n int) []view {
	var cutoff *time.Time
	if n < len(h.events) {
		_, cutoff = h.auditID(n)
	}
	out := make([]view, 0, len(h.tasks))
	for id, t := range h.tasks {
		if s, ok := st.tasks[id]; ok {
			out = append(out, view{id, s})
			continue
		}
		if _, ok := h.head.tasks[id]; ok {
			continue
		}
		if n < len(h.events) && (cutoff == nil || t.CreatedAt.After(*cutoff)) {
			continue
		}
		out = append(out, view{id, taskState{status: t.Status, assignee: deref(t.Assignee), reviewer: deref(t.Reviewer),
			progress: t.ProgressPct, cost: t.CostUsed}})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func counts(vs []view) map[string]int {
	c := map[string]int{}
	for _, v := range vs {
		c[v.ts.status]++
	}
	return c
}

func (h *History) Snapshot(n int) Snapshot {
	st := h.stateAt(n)
	vs := h.present(st, n)
	snap := Snapshot{BriefID: h.briefID, Events: n, Total: len(vs), Counts: counts(vs), Columns: map[string][]Task{}, Agents: []Agent{}}
	snap.AuditID, snap.At = h.auditID(n)
	for _, s := range model.AllStatuses {
		snap.Columns[s] = []Task{}
	}
	agents := map[string]*Agent{}
	agent := func(id string) *Agent {
		a, ok := agents[id]
		if !ok {
			a = &Agent{ID: id, Role: h.roles[id], Assigned: []string{}, Reviewing: []string{}}
			if s, ok := st.agents[id]; ok {
				if a.Role == "" {
					a.Role = s.role
				}
				last := s.last.UTC()
				a.Events, a.LastEventAt = s.events, &last
			}
			agents[id] = a
		}
		return a
	}
	for id := range st.agents {
		agent(id)
	}
	for _, v := range vs {
		row := h.tasks[v.id]
		t := Task{ID: v.id, Title: row.Title, Role: row.Role, Status: v.ts.status, Assignee: ptr(v.ts.assignee), Reviewer: ptr(v.ts.reviewer),
			ProgressPct: v.ts.progress, CostUsed: v.ts.cost, ChangedAuditID: v.ts.auditID}
		if !v.ts.at.IsZero() {
			at := v.ts.at.UTC()
			t.ChangedAt = &at
		}
		snap.Columns[t.Status] = append(snap.Columns[t.Status], t)
		if v.ts.status == model.StatusDone || v.ts.status == model.StatusCancelled {
			continue
		}
		if v.ts.assignee != "" {
			a := agent(v.ts.assignee)
			a.Assigned = append(a.Assigned, v.id)
		}
		if v.ts.reviewer != "" {
			a := agent(v.ts.reviewer)
			a.Reviewing = append(a.Reviewing, v.id)
		}
	}
	for _, col := range snap.Columns {
		slices.SortFunc(col, func(a, b Task) int {
			if c := cmp.Compare(h.tasks[b.ID].Priority, h.tasks[a.ID].Priority); c != 0 {
				return c
			}
			return compareIDs(a.ID, b.ID)
		})
	}
	for _, a := range agents {
		slices.SortFunc(a.Assigned, compareIDs)
		slices.SortFunc(a.Reviewing, compareIDs)
		snap.Agents = append(snap.Agents, *a)
	}
	slices.SortFunc(snap.Agents, func(a, b Agent) int { return cmp.Compare(a.ID, b.ID) })
	return snap
}

var keyStatuses = map[string]bool{
	model.StatusApproved: true, model.StatusChangesRequested: true, model.StatusDone: true,
	model.StatusBlocked: true, model.StatusNeedsOwner: true,
}

func sample[T any](in []T, limit int) []T {
	if len(in) <= limit {
		return in
	}
	out := make([]T, 0, limit)
	for k := 1; k <= limit; k++ {
		out = append(out, in[k*len(in)/limit-1])
	}
	return out
}

func (h *History) Timeline() Timeline {
	tl := Timeline{BriefID: h.briefID, Events: len(h.events), Points: []Point{}, KeyEvents: []KeyEvent{}}
	if len(h.events) > 0 {
		start, end := h.events[0].TS.UTC(), h.events[len(h.events)-1].TS.UTC()
		tl.Start, tl.End = &start, &end
	}
	want := map[int]bool{}
	for _, n := range sample(seq(len(h.events)), MaxPoints) {
		want[n] = true
	}
	var keys []KeyEvent
	st := newState()
	for i, e := range h.events {
		switch e.Tool {
		case "propose_plan", "approve_plan", "reject_plan":
			keys = append(keys, KeyEvent{AuditID: e.ID, At: e.TS.UTC(), Tool: e.Tool, AgentID: e.AgentID})
		}
		h.apply(st, e, func(t Transition) {
			c, ok := t.Changes["status"]
			if !ok || !keyStatuses[str(c.To)] || t.Tool == "approve_plan" || t.Tool == "reject_plan" {
				return
			}
			keys = append(keys, KeyEvent{AuditID: t.AuditID, At: t.At, Tool: t.Tool, AgentID: t.AgentID, TaskID: t.TaskID, From: str(c.From), To: str(c.To)})
		})
		if want[i+1] {
			tl.Points = append(tl.Points, Point{AuditID: e.ID, At: e.TS.UTC(), Counts: counts(h.present(st, i+1))})
		}
	}
	tl.KeyEvents = append(tl.KeyEvents, sample(keys, MaxPoints)...)
	return tl
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func (h *History) Diff(from, to int) Diff {
	d := Diff{BriefID: h.briefID, Transitions: []Transition{}}
	d.From, _ = h.auditID(from)
	d.To, _ = h.auditID(to)
	st := h.stateAt(from).clone()
	emit := func(t Transition) {
		if len(d.Transitions) == MaxTransitions {
			d.Truncated = true
			return
		}
		d.Transitions = append(d.Transitions, t)
	}
	for i := from; i < to && i < len(h.events); i++ {
		h.apply(st, h.events[i], emit)
	}
	return d
}

func compareIDs(a, b string) int {
	na, ea := strconv.Atoi(strings.TrimPrefix(a, "T-"))
	nb, eb := strconv.Atoi(strings.TrimPrefix(b, "T-"))
	if ea == nil && eb == nil && na != nb {
		return cmp.Compare(na, nb)
	}
	return cmp.Compare(a, b)
}
