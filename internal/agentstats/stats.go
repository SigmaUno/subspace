package agentstats

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	TrendDays  = 7
	neutral    = 0.5
	confidence = 3
	expiryNote = "lease expired"
)

type Snapshot struct {
	Now    time.Time
	Agents []*model.Agent
	Tasks  []*model.Task
	Audit  []*model.AuditEvent
	Usage  []usage.Record
}

type TrendPoint struct {
	Day       string `json:"day"`
	Completed int    `json:"completed"`
}

type Stats struct {
	AgentID               string       `json:"agent_id"`
	Role                  string       `json:"role"`
	Provider              string       `json:"provider"`
	Status                string       `json:"status"`
	TasksCompleted        int          `json:"tasks_completed"`
	AvgDurationSeconds    float64      `json:"avg_duration_seconds"`
	P90DurationSeconds    float64      `json:"p90_duration_seconds"`
	ReviewRoundsPerTask   float64      `json:"review_rounds_per_task"`
	FirstPassApprovalRate *float64     `json:"first_pass_approval_rate"`
	Blocked               int          `json:"blocked"`
	Expired               int          `json:"expired"`
	CostPerTask           float64      `json:"cost_per_task"`
	TokensPerTask         float64      `json:"tokens_per_task"`
	Trend                 []TrendPoint `json:"trend"`
	Score                 float64      `json:"score"`
}

type taskFacts struct {
	holder      string
	workSeconds float64
	worked      bool
	rounds      int
	reviewed    bool
	firstPass   bool
	completedAt time.Time
	blocked     map[string]int
	expired     map[string]int
}

type acc struct {
	completed []*model.Task
	facts     []*taskFacts
}

func Build(s Snapshot) []Stats {
	events := map[string][]*model.AuditEvent{}
	for _, e := range s.Audit {
		if e.TaskID != nil {
			events[*e.TaskID] = append(events[*e.TaskID], e)
		}
	}
	byAgent := map[string]*acc{}
	get := func(id string) *acc {
		if byAgent[id] == nil {
			byAgent[id] = &acc{}
		}
		return byAgent[id]
	}
	blocked, expired := map[string]int{}, map[string]int{}
	for _, t := range s.Tasks {
		f := analyze(t, events[t.ID])
		for id, n := range f.blocked {
			blocked[id] += n
		}
		for id, n := range f.expired {
			expired[id] += n
		}
		if t.Assignee == nil || (t.Status != model.StatusDone && t.Status != model.StatusApproved) {
			continue
		}
		if f.completedAt.IsZero() {
			f.completedAt = t.UpdatedAt
		}
		a := get(*t.Assignee)
		a.completed = append(a.completed, t)
		a.facts = append(a.facts, f)
	}

	tokens := map[string]int64{}
	for _, r := range s.Usage {
		tokens[r.TaskID] += r.TokensIn + r.TokensOut
	}

	out := make([]Stats, 0, len(s.Agents))
	for _, ag := range s.Agents {
		st := Stats{
			AgentID: ag.ID, Role: ag.Role, Provider: ag.Provider, Status: ag.Status,
			Blocked: blocked[ag.ID], Expired: expired[ag.ID], Trend: trend(nil, s.Now),
		}
		if a := byAgent[ag.ID]; a != nil {
			fill(&st, a, tokens, s.Now)
		}
		st.Score = Score(st)
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b Stats) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.AgentID, b.AgentID)
	})
	return out
}

func analyze(t *model.Task, evs []*model.AuditEvent) *taskFacts {
	sorted := slices.Clone(evs)
	slices.SortStableFunc(sorted, func(a, b *model.AuditEvent) int {
		if c := a.TS.Compare(b.TS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	f := &taskFacts{blocked: map[string]int{}, expired: map[string]int{}}
	if t.Assignee != nil {
		f.holder = *t.Assignee
	}
	var spanStart time.Time
	inSpan := false
	for _, e := range sorted {
		to, _ := e.After["status"].(string)
		if e.Tool == "claim_task" {
			f.holder = e.AgentID
		}
		if to == "" {
			continue
		}
		if inSpan && to != model.StatusInProgress {
			f.workSeconds += e.TS.Sub(spanStart).Seconds()
			f.worked, inSpan = true, false
		}
		switch {
		case to == model.StatusInProgress && !inSpan:
			spanStart, inSpan = e.TS, true
		case to == model.StatusBlocked:
			f.blocked[f.holder]++
		}
		if e.Tool == "lease_release" && e.Note != nil && *e.Note == expiryNote {
			f.expired[f.holder]++
		}
		if e.Tool == "review" {
			if !f.reviewed {
				f.reviewed, f.firstPass = true, to == model.StatusApproved
			}
			if to != model.StatusApproved {
				f.rounds++
			}
		}
		if f.completedAt.IsZero() && (to == model.StatusApproved || to == model.StatusDone) {
			f.completedAt = e.TS
		}
	}
	return f
}

func fill(st *Stats, a *acc, tokens map[string]int64, now time.Time) {
	n := len(a.completed)
	st.TasksCompleted = n
	var cost float64
	var tok int64
	var rounds int
	var durations []float64
	var reviewed, first int
	var done []time.Time
	for i, t := range a.completed {
		f := a.facts[i]
		cost += t.CostUsed
		tok += tokens[t.ID]
		rounds += f.rounds
		if f.worked {
			durations = append(durations, f.workSeconds)
		}
		if f.reviewed {
			reviewed++
			if f.firstPass {
				first++
			}
		}
		done = append(done, f.completedAt)
	}
	st.CostPerTask = round(cost/float64(n), 4)
	st.TokensPerTask = round(float64(tok)/float64(n), 1)
	st.ReviewRoundsPerTask = round(float64(rounds)/float64(n), 2)
	if reviewed > 0 {
		r := round(float64(first)/float64(reviewed), 4)
		st.FirstPassApprovalRate = &r
	}
	if len(durations) > 0 {
		slices.Sort(durations)
		var sum float64
		for _, d := range durations {
			sum += d
		}
		st.AvgDurationSeconds = round(sum/float64(len(durations)), 1)
		st.P90DurationSeconds = round(percentile(durations, 0.9), 1)
	}
	st.Trend = trend(done, now)
}

func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(rank, 0)]
}

func trend(done []time.Time, now time.Time) []TrendPoint {
	today := now.UTC().Truncate(24 * time.Hour)
	out := make([]TrendPoint, TrendDays)
	for i := range out {
		out[i].Day = today.AddDate(0, 0, i-TrendDays+1).Format(time.DateOnly)
	}
	for _, at := range done {
		day := at.UTC().Format(time.DateOnly)
		for i := range out {
			if out[i].Day == day {
				out[i].Completed++
			}
		}
	}
	return out
}

func Score(s Stats) float64 {
	n := s.TasksCompleted + s.Blocked + s.Expired
	if n == 0 {
		return neutral
	}
	quality := neutral
	if s.FirstPassApprovalRate != nil {
		quality = *s.FirstPassApprovalRate
	}
	reliability := float64(s.TasksCompleted) / float64(n)
	rounds := 1 / (1 + s.ReviewRoundsPerTask)
	raw := 0.5*quality + 0.3*reliability + 0.2*rounds
	weight := float64(n) / float64(n+confidence)
	return round(neutral+(raw-neutral)*weight, 4)
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}
