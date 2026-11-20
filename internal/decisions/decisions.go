package decisions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	KindQuestion = "question"
	KindPlan     = "plan"
	KindMerge    = "merge"
	KindUnblock  = "unblock"
	KindStale    = "stale_agent"
	KindBudget   = "budget"
)

const (
	ActionAnswer  = "answer"
	ActionApprove = "approve"
	ActionReject  = "reject"
	ActionMerge   = "merge"
	ActionUnblock = "unblock"
	ActionSkip    = "skip"
)

const (
	defaultStaleAfter = 10 * time.Minute
	defaultSnooze     = time.Hour
	maxSnooze         = 7 * 24 * time.Hour
	maxAgeBonus       = 60
	agePerPoint       = 5 * time.Minute
	needsOwnerBonus   = 15
)

var impact = map[string]int{
	KindPlan:     80,
	KindQuestion: 70,
	KindBudget:   65,
	KindMerge:    60,
	KindUnblock:  50,
	KindStale:    40,
}

type Links struct {
	Thread string `json:"thread,omitempty"`
	PR     string `json:"pr,omitempty"`
}

type Decision struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	BriefID   string    `json:"brief_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	Links     Links     `json:"links"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
	Actions   []string  `json:"actions"`
}

type Snapshot struct {
	Questions []*model.Question
	Plans     []*model.Plan
	Tasks     []*model.Task
	Agents    []*model.Agent
	Extra     []*Decision
}

type Source interface {
	Snapshot(ctx context.Context) (*Snapshot, error)
}

type Actions interface {
	AnswerQuestion(ctx context.Context, id, answer string) (*model.Question, error)
	ApprovePlan(ctx context.Context, id, note string) (*model.Plan, error)
	RejectPlan(ctx context.Context, id, reason string) (*model.Plan, error)
	ApproveMerge(ctx context.Context, taskID, note string) (*model.Task, error)
	Unblock(ctx context.Context, taskID, to, note string) (*model.Task, error)
	Skip(ctx context.Context, decisionID string, until time.Time) error
}

type Options struct {
	StaleAfter time.Duration
	Now        func() time.Time
}

type Feed struct {
	src  Source
	act  Actions
	opt  Options
	mu   sync.Mutex
	snoz map[string]time.Time
}

type Outcome struct {
	ID     string    `json:"id"`
	Action string    `json:"action"`
	Result any       `json:"result"`
	Next   *Decision `json:"next"`
}

func New(src Source, act Actions, o Options) *Feed {
	if o.StaleAfter <= 0 {
		o.StaleAfter = defaultStaleAfter
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Feed{src: src, act: act, opt: o, snoz: map[string]time.Time{}}
}

func (f *Feed) List(ctx context.Context, limit int) ([]*Decision, error) {
	all, err := f.collect(ctx)
	if err != nil {
		return nil, err
	}
	now := f.opt.Now()
	out := make([]*Decision, 0, len(all))
	f.mu.Lock()
	for id, until := range f.snoz {
		if !now.Before(until) {
			delete(f.snoz, id)
		}
	}
	for _, d := range all {
		if _, ok := f.snoz[d.ID]; !ok {
			out = append(out, d)
		}
	}
	f.mu.Unlock()
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Feed) Next(ctx context.Context) (*Decision, error) {
	ds, err := f.List(ctx, 1)
	if err != nil || len(ds) == 0 {
		return nil, err
	}
	return ds[0], nil
}

type payload struct {
	Answer string `json:"answer"`
	Note   string `json:"note"`
	Reason string `json:"reason"`
	Status string `json:"status"`
	Snooze string `json:"snooze"`
}

func (f *Feed) Act(ctx context.Context, id, action string, raw json.RawMessage) (*Outcome, error) {
	var p payload
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, model.BadRequest("invalid payload: %v", err)
		}
	}
	all, err := f.collect(ctx)
	if err != nil {
		return nil, err
	}
	var d *Decision
	for _, x := range all {
		if x.ID == id {
			d = x
			break
		}
	}
	if d == nil {
		return nil, model.NotFound("decision %s not found", id)
	}
	if !contains(d.Actions, action) {
		return nil, model.Conflict("action %q not available for %s (allowed: %s)", action, id, strings.Join(d.Actions, ", "))
	}
	res, err := f.dispatch(ctx, d, action, p)
	if err != nil {
		return nil, err
	}
	next, err := f.Next(ctx)
	if err != nil {
		return nil, err
	}
	return &Outcome{ID: id, Action: action, Result: res, Next: next}, nil
}

func (f *Feed) dispatch(ctx context.Context, d *Decision, action string, p payload) (any, error) {
	ref := d.ID[strings.IndexByte(d.ID, ':')+1:]
	switch action {
	case ActionAnswer:
		if strings.TrimSpace(p.Answer) == "" {
			return nil, model.BadRequest("answer is required")
		}
		return f.act.AnswerQuestion(ctx, ref, p.Answer)
	case ActionApprove:
		return f.act.ApprovePlan(ctx, ref, p.Note)
	case ActionReject:
		reason := p.Reason
		if reason == "" {
			reason = p.Note
		}
		return f.act.RejectPlan(ctx, ref, reason)
	case ActionMerge:
		return f.act.ApproveMerge(ctx, ref, p.Note)
	case ActionUnblock:
		to := p.Status
		if to == "" {
			to = model.StatusReady
			if d.AgentID != "" {
				to = model.StatusAssigned
			}
		}
		return f.act.Unblock(ctx, ref, to, p.Note)
	case ActionSkip:
		dur := defaultSnooze
		if p.Snooze != "" {
			v, err := time.ParseDuration(p.Snooze)
			if err != nil || v <= 0 || v > maxSnooze {
				return nil, model.BadRequest("snooze must be a duration between 0 and %s", maxSnooze)
			}
			dur = v
		}
		until := f.opt.Now().Add(dur)
		if err := f.act.Skip(ctx, d.ID, until); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.snoz[d.ID] = until
		f.mu.Unlock()
		return map[string]any{"snoozed_until": until.UTC()}, nil
	}
	return nil, model.BadRequest("unknown action %q", action)
}

func (f *Feed) collect(ctx context.Context) ([]*Decision, error) {
	s, err := f.src.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	now := f.opt.Now()
	var out []*Decision
	add := func(d *Decision, extra int) {
		d.Priority = impact[d.Kind] + extra + ageBonus(now.Sub(d.CreatedAt))
		d.Actions = append(d.Actions, ActionSkip)
		out = append(out, d)
	}
	for _, q := range s.Questions {
		if q.Status != "open" {
			continue
		}
		d := &Decision{
			ID: "q:" + q.ID, Kind: KindQuestion, Title: q.Question, AgentID: q.AgentID,
			BriefID: deref(q.BriefID), TaskID: deref(q.TaskID), CreatedAt: q.CreatedAt,
			Summary: fmt.Sprintf("%s asks", q.AgentID), Actions: []string{ActionAnswer},
			Links: Links{Thread: model.ThreadAgent + ":" + q.AgentID},
		}
		if len(q.Options) > 0 {
			d.Summary += "; options: " + strings.Join(q.Options, " | ")
		}
		if q.TaskID != nil {
			d.Links.Thread = model.ThreadTask + ":" + *q.TaskID
		}
		add(d, 0)
	}
	for _, p := range s.Plans {
		if p.Status != model.PlanProposed {
			continue
		}
		d := &Decision{
			ID: "plan:" + p.ID, Kind: KindPlan, BriefID: p.BriefID, AgentID: p.ProposedBy, CreatedAt: p.CreatedAt,
			Title:   fmt.Sprintf("Approve plan %s for %s", p.ID, p.BriefID),
			Summary: fmt.Sprintf("proposed by %s", p.ProposedBy), Actions: []string{ActionApprove, ActionReject},
			Links: Links{Thread: model.ThreadBrief + ":" + p.BriefID},
		}
		if len(p.Tasks) > 0 {
			d.Summary += fmt.Sprintf(", %d tasks", len(p.Tasks))
		}
		if p.Note != nil && *p.Note != "" {
			d.Summary += ": " + *p.Note
		}
		add(d, 0)
	}
	for _, t := range s.Tasks {
		d := &Decision{
			TaskID: t.ID, BriefID: t.BriefID, AgentID: deref(t.Assignee), CreatedAt: t.UpdatedAt,
			Links: Links{Thread: model.ThreadTask + ":" + t.ID, PR: deref(t.PRURL)},
		}
		extra := 0
		switch t.Status {
		case model.StatusApproved:
			d.ID, d.Kind, d.Actions = "merge:"+t.ID, KindMerge, []string{ActionMerge}
			d.Title = fmt.Sprintf("Merge %s: %s", t.ID, t.Title)
			d.Summary = mergeSummary(t)
		case model.StatusBlocked, model.StatusNeedsOwner:
			d.ID, d.Kind, d.Actions = "unblock:"+t.ID, KindUnblock, []string{ActionUnblock}
			d.Title = fmt.Sprintf("Unblock %s: %s", t.ID, t.Title)
			d.Summary = t.Status
			if r := deref(t.BlockedReason); r != "" {
				d.Summary += ": " + r
			}
			if t.Status == model.StatusNeedsOwner {
				extra = needsOwnerBonus
			}
		default:
			continue
		}
		add(d, extra)
	}
	for _, d := range s.Extra {
		add(d, 0)
	}
	for _, a := range s.Agents {
		if a.Status != model.AgentActive || a.CurrentTaskID == nil {
			continue
		}
		var seen time.Time
		if a.LastSeenAt != nil {
			seen = *a.LastSeenAt
		}
		idle := now.Sub(seen)
		if a.LastSeenAt != nil && idle < f.opt.StaleAfter {
			continue
		}
		since := "never seen"
		if a.LastSeenAt != nil {
			since = "last seen " + idle.Truncate(time.Second).String() + " ago"
		} else {
			seen = a.CreatedAt
		}
		add(&Decision{
			ID: "stale:" + a.ID, Kind: KindStale, AgentID: a.ID, TaskID: *a.CurrentTaskID, CreatedAt: seen,
			Title:   fmt.Sprintf("Agent %s is stale on %s", a.ID, *a.CurrentTaskID),
			Summary: since, Links: Links{Thread: model.ThreadAgent + ":" + a.ID},
		}, 0)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out, nil
}

func mergeSummary(t *model.Task) string {
	parts := []string{"approved"}
	if t.Reviewer != nil {
		parts[0] += " by " + *t.Reviewer
	}
	if t.PRURL == nil {
		parts = append(parts, "no PR")
	}
	if t.Tests != nil {
		parts = append(parts, fmt.Sprintf("tests %d passed, %d failed", t.Tests.Passed, t.Tests.Failed))
	}
	if t.ReviewRounds > 0 {
		parts = append(parts, fmt.Sprintf("%d review rounds", t.ReviewRounds))
	}
	return strings.Join(parts, ", ")
}

func ageBonus(age time.Duration) int {
	if age <= 0 {
		return 0
	}
	return min(int(age/agePerPoint), maxAgeBonus)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
