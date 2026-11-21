package decisions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
)

type fake struct {
	mu     sync.Mutex
	snap   decisions.Snapshot
	calls  []string
	queued map[string]bool
}

func (f *fake) Snapshot(context.Context) (*decisions.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	return &s, nil
}

func (f *fake) record(s string) {
	f.calls = append(f.calls, s)
}

func (f *fake) AnswerQuestion(_ context.Context, id, answer string) (*model.Question, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, q := range f.snap.Questions {
		if q.ID == id {
			q.Status, q.Answer = "answered", &answer
			f.record("answer " + id + " " + answer)
			return q, nil
		}
	}
	return nil, model.NotFound("question %s not found", id)
}

func (f *fake) ApprovePlan(_ context.Context, id, note string) (*model.Plan, error) {
	return f.decide(id, model.PlanApproved, note)
}

func (f *fake) RejectPlan(_ context.Context, id, reason string) (*model.Plan, error) {
	return f.decide(id, model.PlanRejected, reason)
}

func (f *fake) decide(id, status, note string) (*model.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.snap.Plans {
		if p.ID == id {
			p.Status = status
			f.record(status + " " + id + " " + note)
			return p, nil
		}
	}
	return nil, model.NotFound("plan %s not found", id)
}

func (f *fake) MergeAction(_ context.Context, t *model.Task) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch queued, ok := f.queued[t.ID]; {
	case !ok:
		return decisions.ActionMarkDone, nil
	case queued:
		return "", nil
	}
	return decisions.ActionMerge, nil
}

func (f *fake) Merge(_ context.Context, id, note string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[id] = true
	f.record("enqueue " + id + " " + note)
	return map[string]string{"task_id": id, "state": "queued"}, nil
}

func (f *fake) MarkDone(_ context.Context, id, note string) (*model.Task, error) {
	return f.move(id, model.StatusDone, "mark_done "+id+" "+note)
}

func (f *fake) Unblock(_ context.Context, id, to, note string) (*model.Task, error) {
	return f.move(id, to, "unblock "+id+" "+to+" "+note)
}

func (f *fake) move(id, to, call string) (*model.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.snap.Tasks {
		if t.ID == id {
			t.Status = to
			f.record(call)
			return t, nil
		}
	}
	return nil, model.NotFound("task %s not found", id)
}

func (f *fake) RotateToken(_ context.Context, id string, expiresIn time.Duration) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("rotate " + id + " " + expiresIn.String())
	return nil, nil
}

func (f *fake) Skip(_ context.Context, id string, until time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("skip " + id + " " + until.UTC().Format(time.RFC3339))
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func sp(s string) *string { return &s }

func tp(t time.Time) *time.Time { return &t }

type env struct {
	t   *testing.T
	srv *httptest.Server
	src *fake
	clk *clock
}

func setup(t *testing.T) *env {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clk := &clock{t: t0}
	src := &fake{snap: decisions.Snapshot{
		Questions: []*model.Question{
			{ID: "Q-1", BriefID: sp("B-1"), TaskID: sp("T-3"), AgentID: "claude-1", Question: "Use JWT or sessions?",
				Options: []string{"jwt", "sessions"}, Status: "open", CreatedAt: t0.Add(-30 * time.Minute)},
			{ID: "Q-2", AgentID: "codex-1", Question: "old", Status: "answered", CreatedAt: t0.Add(-time.Hour)},
		},
		Plans: []*model.Plan{
			{ID: "P-1", BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", Note: sp("three tasks"),
				Tasks: make([]*model.Task, 3), CreatedAt: t0.Add(-5 * time.Minute)},
			{ID: "P-0", BriefID: "B-1", Status: model.PlanSuperseded, ProposedBy: "rex", CreatedAt: t0.Add(-time.Hour)},
		},
		Tasks: []*model.Task{
			{ID: "T-1", BriefID: "B-1", Title: "Login", Status: model.StatusApproved, Assignee: sp("claude-1"),
				Reviewer: sp("codex-r"), PRURL: sp("https://git/pr/1"), Tests: &model.TestResult{Passed: 10},
				UpdatedAt: t0.Add(-4 * time.Hour)},
			{ID: "T-2", BriefID: "B-1", Title: "Budget", Status: model.StatusBlocked, Assignee: sp("codex-1"),
				BlockedReason: sp("budget_exceeded"), UpdatedAt: t0.Add(-10 * time.Minute)},
			{ID: "T-4", BriefID: "B-1", Title: "Loop", Status: model.StatusNeedsOwner,
				BlockedReason: sp("3 review rounds"), UpdatedAt: t0},
			{ID: "T-5", BriefID: "B-1", Title: "Busy", Status: model.StatusInProgress, UpdatedAt: t0},
		},
		Agents: []*model.Agent{
			{ID: "claude-1", Status: model.AgentActive, CurrentTaskID: sp("T-3"), LastSeenAt: tp(t0.Add(-time.Minute))},
			{ID: "oc-1", Status: model.AgentActive, CurrentTaskID: sp("T-6"), LastSeenAt: tp(t0.Add(-20 * time.Minute))},
			{ID: "oc-2", Status: model.AgentActive, LastSeenAt: tp(t0.Add(-time.Hour))},
			{ID: "oc-3", Status: model.AgentPaused, CurrentTaskID: sp("T-7"), LastSeenAt: tp(t0.Add(-time.Hour))},
		},
	}}
	feed := decisions.New(src, src, decisions.Options{Now: clk.Now, StaleAfter: 10 * time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/decisions", http.StripPrefix("/api/v1", decisions.Handler(feed, nil)))
	mux.Handle("/api/v1/decisions/", http.StripPrefix("/api/v1", decisions.Handler(feed, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, src: src, clk: clk}
}

func (e *env) do(method, path string, body any, out any) int {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.srv.URL+"/api/v1"+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (e *env) ids() []string {
	e.t.Helper()
	var r struct{ Decisions []decisions.Decision }
	if c := e.do("GET", "/decisions", nil, &r); c != 200 {
		e.t.Fatalf("list: %d", c)
	}
	ids := make([]string, len(r.Decisions))
	for i, d := range r.Decisions {
		ids[i] = d.ID
	}
	return ids
}

func (e *env) next() *decisions.Decision {
	e.t.Helper()
	var r struct{ Decision *decisions.Decision }
	if c := e.do("GET", "/decisions/next", nil, &r); c != 200 {
		e.t.Fatalf("next: %d", c)
	}
	return r.Decision
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestInboxFlow(t *testing.T) {
	e := setup(t)

	var full struct{ Decisions []decisions.Decision }
	e.do("GET", "/decisions", nil, &full)
	byID := map[string]decisions.Decision{}
	for _, d := range full.Decisions {
		byID[d.ID] = d
	}
	eq(t, e.ids(), []string{"merge:T-1", "plan:P-1", "q:Q-1", "unblock:T-4", "unblock:T-2", "stale:oc-1"})

	m := byID["merge:T-1"]
	if m.Priority != 60+48 || m.Links.PR != "https://git/pr/1" || m.Links.Thread != "task:T-1" || m.AgentID != "claude-1" {
		t.Fatalf("merge decision: %+v", m)
	}
	eq(t, m.Actions, []string{"mark_done", "skip"})
	eq(t, byID["plan:P-1"].Actions, []string{"approve", "reject", "skip"})
	if q := byID["q:Q-1"]; q.TaskID != "T-3" || q.BriefID != "B-1" || q.Links.Thread != "task:T-3" {
		t.Fatalf("question decision: %+v", q)
	}
	if s := byID["stale:oc-1"]; s.TaskID != "T-6" || s.Summary != "last seen 20m0s ago" {
		t.Fatalf("stale decision: %+v", s)
	}

	var lim struct{ Decisions []decisions.Decision }
	e.do("GET", "/decisions?limit=2", nil, &lim)
	if len(lim.Decisions) != 2 {
		t.Fatalf("limit: %d", len(lim.Decisions))
	}

	if d := e.next(); d.ID != "merge:T-1" {
		t.Fatalf("next: %s", d.ID)
	}

	var out decisions.Outcome
	if c := e.do("POST", "/decisions/merge:T-1", map[string]string{"action": "merge", "note": "merged #1"}, &out); c != 200 {
		t.Fatalf("merge: %d", c)
	}
	if out.Next == nil || out.Next.ID != "plan:P-1" {
		t.Fatalf("outcome next: %+v", out.Next)
	}

	var errb struct{ Error struct{ Code string } }
	if c := e.do("POST", "/decisions/q:Q-1", map[string]string{"action": "answer"}, &errb); c != 400 || errb.Error.Code != "bad_request" {
		t.Fatalf("empty answer: %d %+v", c, errb)
	}
	if c := e.do("POST", "/decisions/q:Q-1", map[string]string{"action": "approve"}, &errb); c != 409 {
		t.Fatalf("wrong action: %d", c)
	}
	if c := e.do("POST", "/decisions/q:Q-2", map[string]string{"action": "answer", "answer": "x"}, &errb); c != 404 {
		t.Fatalf("answered question: %d", c)
	}
	if c := e.do("POST", "/decisions/merge:T-1", map[string]string{"action": "merge"}, &errb); c != 404 {
		t.Fatalf("repeat merge: %d", c)
	}
	if c := e.do("POST", "/decisions/q:Q-1", map[string]string{}, &errb); c != 400 {
		t.Fatalf("missing action: %d", c)
	}

	e.do("POST", "/decisions/q:Q-1", map[string]string{"action": "answer", "answer": "jwt"}, nil)
	e.do("POST", "/decisions/plan:P-1", map[string]string{"action": "reject", "reason": "too big"}, nil)
	e.do("POST", "/decisions/unblock:T-2", map[string]string{"action": "unblock", "note": "raised budget"}, nil)
	e.do("POST", "/decisions/unblock:T-4", map[string]string{"action": "unblock"}, nil)
	if c := e.do("POST", "/decisions/stale:oc-1", map[string]string{"action": "skip", "snooze": "forever"}, &errb); c != 400 {
		t.Fatalf("bad snooze: %d", c)
	}
	if c := e.do("POST", "/decisions/stale:oc-1", map[string]string{"action": "skip", "snooze": "30m"}, &out); c != 200 || out.Next != nil {
		t.Fatalf("skip: %d %+v", c, out.Next)
	}
	eq(t, e.ids(), []string{})
	if d := e.next(); d != nil {
		t.Fatalf("expected empty inbox, got %s", d.ID)
	}

	e.clk.Add(30 * time.Minute)
	eq(t, e.ids(), []string{"stale:oc-1", "stale:claude-1"})

	eq(t, e.src.calls, []string{
		"mark_done T-1 merged #1",
		"answer Q-1 jwt",
		"rejected P-1 too big",
		"unblock T-2 assigned raised budget",
		"unblock T-4 ready ",
		"skip stale:oc-1 2026-11-20T12:30:00Z",
	})
}

func TestMergeDecisionEnqueuesWhenTheRepoHasAQueue(t *testing.T) {
	e := setup(t)
	e.src.queued = map[string]bool{"T-1": false}

	var full struct{ Decisions []decisions.Decision }
	e.do("GET", "/decisions", nil, &full)
	if len(full.Decisions) == 0 || full.Decisions[0].ID != "merge:T-1" {
		t.Fatalf("decisions %+v", full.Decisions)
	}
	eq(t, full.Decisions[0].Actions, []string{"merge", "skip"})

	var errb struct{ Error struct{ Code string } }
	if c := e.do("POST", "/decisions/merge:T-1", map[string]string{"action": "mark_done"}, &errb); c != 409 {
		t.Fatalf("mark_done with a queue: %d", c)
	}
	var out struct {
		Result map[string]string
	}
	if c := e.do("POST", "/decisions/merge:T-1", map[string]string{"action": "merge", "note": "ship"}, &out); c != 200 || out.Result["state"] != "queued" {
		t.Fatalf("enqueue: %d %+v", c, out)
	}
	if c := e.do("POST", "/decisions/merge:T-1", map[string]string{"action": "merge"}, &errb); c != 404 {
		t.Fatalf("queued task still offered: %d", c)
	}
	eq(t, e.src.calls, []string{"enqueue T-1 ship"})
	if s := e.src.snap.Tasks[0].Status; s != model.StatusApproved {
		t.Fatalf("task moved to %s", s)
	}
}
