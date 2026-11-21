package report

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	unassigned      = "unassigned"
	unknownProvider = "unknown"
	minOutlierSet   = 4
)

func Build(s Snapshot) Report {
	tasks := slices.Clone(s.Tasks)
	slices.SortFunc(tasks, func(a, b *model.Task) int { return compareIDs(a.ID, b.ID) })

	events := map[string][]*model.AuditEvent{}
	for _, e := range s.Audit {
		if e.TaskID != nil {
			events[*e.TaskID] = append(events[*e.TaskID], e)
		}
	}

	r := Report{
		BriefID:     s.Brief.ID,
		Title:       s.Brief.Title,
		GeneratedAt: s.Now.UTC(),
		Total:       len(tasks),
		Counts:      map[string]int{},
		Tasks:       make([]TaskReport, 0, len(tasks)),
		Cost:        Cost{ByTask: map[string]float64{}, ByAgent: map[string]float64{}, ByProvider: map[string]float64{}},
	}
	for _, t := range tasks {
		tr := buildTask(t, events[t.ID], s.Agents, s.Now)
		r.Tasks = append(r.Tasks, tr)
		r.Counts[t.Status]++
		r.Cost.TotalEUR += tr.CostEUR
		r.Cost.ByTask[tr.ID] = round(tr.CostEUR)
		r.Cost.ByAgent[agentKey(tr.Assignee)] += tr.CostEUR
		r.Cost.ByProvider[tr.Provider] += tr.CostEUR
		r.BlockedSeconds += tr.BlockedSeconds
	}
	r.Cost.TotalEUR = round(r.Cost.TotalEUR)
	roundAll(r.Cost.ByAgent)
	roundAll(r.Cost.ByProvider)
	r.Throughput = throughput(r.Tasks, s.Now)
	r.CriticalPath = criticalPath(tasks, r.Tasks)
	r.Outliers = outliers(r.Tasks)
	r.Efficiency = efficiency(tasks, r.Tasks, events, s.PRs)
	return r
}

func buildTask(t *model.Task, evs []*model.AuditEvent, agents map[string]string, now time.Time) TaskReport {
	tr := TaskReport{
		ID: t.ID, Title: t.Title, Status: t.Status, CostEUR: t.CostUsed, CreatedAt: t.CreatedAt.UTC(),
		Provider: unknownProvider, Reviews: []Review{}, TimeInStatus: map[string]int64{},
	}
	if t.Assignee != nil {
		tr.Assignee = *t.Assignee
		if p, ok := agents[tr.Assignee]; ok && p != "" {
			tr.Provider = p
		}
	}
	tr.Timeline = steps(t, evs)

	var lastSubmit time.Time
	for _, st := range tr.Timeline {
		at := st.At
		switch {
		case st.Tool == "claim_task" && st.To == model.StatusInProgress:
			tr.Claims++
			if tr.ClaimedAt == nil {
				tr.ClaimedAt = &at
			}
		case st.Tool == "submit":
			lastSubmit = at
			if tr.SubmittedAt == nil {
				tr.SubmittedAt = &at
			}
		case st.Tool == "review":
			rv := Review{At: at, Reviewer: st.By, Verdict: "changes_requested"}
			if st.To == model.StatusApproved {
				rv.Verdict = "approve"
			} else {
				tr.ReviewRounds++
			}
			if !lastSubmit.IsZero() {
				rv.WaitSeconds = seconds(at.Sub(lastSubmit))
			}
			tr.Reviews = append(tr.Reviews, rv)
		}
		if st.To == model.StatusDone {
			tr.DoneAt = &at
		}
	}
	if tr.DoneAt == nil && t.Status == model.StatusDone {
		at := t.UpdatedAt.UTC()
		tr.DoneAt = &at
	}

	cur, since := t.Status, tr.CreatedAt
	if len(tr.Timeline) > 0 {
		cur = tr.Timeline[0].From
	}
	for _, st := range tr.Timeline {
		tr.TimeInStatus[cur] += seconds(st.At.Sub(since))
		cur, since = st.To, st.At
	}
	end := since
	if !closed(cur) {
		end = now
		tr.TimeInStatus[cur] += seconds(now.Sub(since))
	}
	if tr.DoneAt != nil {
		end = *tr.DoneAt
	}

	queueEnd := end
	if tr.ClaimedAt != nil {
		queueEnd = *tr.ClaimedAt
		tr.ActiveSeconds = seconds(end.Sub(*tr.ClaimedAt))
	}
	tr.QueueSeconds = seconds(queueEnd.Sub(tr.CreatedAt))
	tr.LeadSeconds = seconds(end.Sub(tr.CreatedAt))
	tr.WorkSeconds = tr.TimeInStatus[model.StatusInProgress]
	tr.ReviewSeconds = tr.TimeInStatus[model.StatusSubmitted] + tr.TimeInStatus[model.StatusInReview]
	tr.BlockedSeconds = tr.TimeInStatus[model.StatusBlocked]
	return tr
}

func steps(t *model.Task, evs []*model.AuditEvent) []Step {
	sorted := slices.Clone(evs)
	slices.SortStableFunc(sorted, func(a, b *model.AuditEvent) int {
		if c := a.TS.Compare(b.TS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	out := []Step{}
	prev := t.CreatedAt.UTC()
	for _, e := range sorted {
		from, ok1 := e.Before["status"].(string)
		to, ok2 := e.After["status"].(string)
		if !ok1 || !ok2 {
			continue
		}
		at := e.TS.UTC()
		out = append(out, Step{At: at, From: from, To: to, By: e.AgentID, Tool: e.Tool, ElapsedSeconds: seconds(at.Sub(prev))})
		prev = at
	}
	return out
}

func throughput(tasks []TaskReport, now time.Time) Throughput {
	th := Throughput{Hourly: []Bucket{}}
	if len(tasks) == 0 {
		return th
	}
	start := tasks[0].CreatedAt
	end := now
	open := false
	var lastDone time.Time
	for _, t := range tasks {
		start = minTime(start, t.CreatedAt)
		switch {
		case t.DoneAt != nil:
			th.Done++
			lastDone = maxTime(lastDone, *t.DoneAt)
		case t.Status != model.StatusCancelled:
			open = true
		}
	}
	if !open && th.Done > 0 {
		end = lastDone
	}
	window := end.Sub(start)
	th.WindowHours = round(math.Max(window.Hours(), 0))
	th.PerHour = round(float64(th.Done) / math.Max(window.Hours(), 1))

	counts := map[time.Time]int{}
	for _, t := range tasks {
		if t.DoneAt != nil {
			counts[t.DoneAt.Truncate(time.Hour)]++
		}
	}
	for h := start.Truncate(time.Hour); !h.After(end); h = h.Add(time.Hour) {
		th.Hourly = append(th.Hourly, Bucket{Hour: h, Done: counts[h]})
	}
	return th
}

func criticalPath(tasks []*model.Task, reports []TaskReport) CriticalPath {
	type node struct {
		weight  int64
		cost    int64
		depth   int
		prev    int
		state   int
		present bool
	}
	index := map[string]int{}
	nodes := make([]node, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
		if t.Status != model.StatusCancelled {
			nodes[i] = node{weight: reports[i].ActiveSeconds, prev: -1, present: true}
		}
	}
	var visit func(i int)
	visit = func(i int) {
		n := &nodes[i]
		n.state = 1
		n.cost, n.depth = n.weight, 1
		for _, dep := range tasks[i].DependsOn {
			j, ok := index[dep]
			if !ok || !nodes[j].present || nodes[j].state == 1 {
				continue
			}
			if nodes[j].state == 0 {
				visit(j)
			}
			c, d := nodes[j].cost+n.weight, nodes[j].depth+1
			if n.prev < 0 || c > n.cost || (c == n.cost && d > n.depth) {
				n.cost, n.depth, n.prev = c, d, j
			}
		}
		n.state = 2
	}
	best := -1
	for i := range nodes {
		if !nodes[i].present {
			continue
		}
		if nodes[i].state == 0 {
			visit(i)
		}
		if best < 0 || nodes[i].cost > nodes[best].cost || (nodes[i].cost == nodes[best].cost && nodes[i].depth > nodes[best].depth) {
			best = i
		}
	}
	cp := CriticalPath{TaskIDs: []string{}}
	if best < 0 {
		return cp
	}
	cp.Seconds = nodes[best].cost
	for i := best; i >= 0; i = nodes[i].prev {
		cp.TaskIDs = append(cp.TaskIDs, tasks[i].ID)
	}
	slices.Reverse(cp.TaskIDs)
	return cp
}

func outliers(tasks []TaskReport) []Outlier {
	var duration, cost []Outlier
	for _, t := range tasks {
		if t.ClaimedAt != nil {
			duration = append(duration, Outlier{TaskID: t.ID, Metric: "active_seconds", Value: float64(t.ActiveSeconds)})
		}
		if t.Assignee != "" {
			cost = append(cost, Outlier{TaskID: t.ID, Metric: "cost_eur", Value: t.CostEUR})
		}
	}
	out := []Outlier{}
	for _, set := range [][]Outlier{duration, cost} {
		out = append(out, fence(set)...)
	}
	return out
}

func fence(set []Outlier) []Outlier {
	if len(set) < minOutlierSet {
		return nil
	}
	vals := make([]float64, len(set))
	for i, o := range set {
		vals[i] = o.Value
	}
	slices.Sort(vals)
	q1, q3 := quantile(vals, 0.25), quantile(vals, 0.75)
	limit := q3 + 1.5*(q3-q1)
	var out []Outlier
	for _, o := range set {
		if o.Value > limit && o.Value > 0 {
			o.Threshold = round(limit)
			o.Value = round(o.Value)
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b Outlier) int { return cmp.Compare(b.Value, a.Value) })
	return out
}

func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func closed(status string) bool {
	return status == model.StatusDone || status == model.StatusCancelled
}

func seconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

func round(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func roundAll(m map[string]float64) {
	for k, v := range m {
		m[k] = round(v)
	}
}

func agentKey(id string) string {
	if id == "" {
		return unassigned
	}
	return id
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func compareIDs(a, b string) int {
	na, ea := strconv.Atoi(strings.TrimPrefix(a, "T-"))
	nb, eb := strconv.Atoi(strings.TrimPrefix(b, "T-"))
	if ea == nil && eb == nil && na != nb {
		return cmp.Compare(na, nb)
	}
	return cmp.Compare(a, b)
}
