package forecast

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	defaultRuns    = 1000
	maxRuns        = 20000
	defaultSigma   = 0.5
	minSigma       = 0.1
	maxSigma       = 1.0
	defaultMinutes = 60.0
	budgetFraction = 0.5
	reviewMinutes  = 10.0
	burnWindow     = time.Hour
	minBurnHours   = 1.0 / 60
	overrunHigh    = 0.5
	overrunMedium  = 0.1
)

const (
	CodeCostExceeded = "cost_exceeded"
	CodeCostOverrun  = "cost_overrun"
	CodePathSlip     = "critical_path_slip"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
)

type Options struct {
	Runs     int
	Seed     uint64
	EURToUSD float64
}

type Snapshot struct {
	Now   time.Time
	Brief model.Brief
	Tasks []*model.Task
	Usage []usage.Record
}

type Range struct {
	P10 float64 `json:"p10"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
}

type TimeRange struct {
	P10 time.Time `json:"p10"`
	P50 time.Time `json:"p50"`
	P90 time.Time `json:"p90"`
}

type Counts struct {
	Total     int `json:"total"`
	Complete  int `json:"complete"`
	Remaining int `json:"remaining"`
}

type Cost struct {
	Used              float64 `json:"used"`
	Budget            float64 `json:"budget"`
	Projected         Range   `json:"projected"`
	ExceedProbability float64 `json:"exceed_probability"`
}

type Schedule struct {
	RemainingMinutes    Range     `json:"remaining_minutes"`
	CompletesAt         TimeRange `json:"completes_at"`
	CriticalPathMinutes Range     `json:"critical_path_minutes"`
	PlannedCriticalPath float64   `json:"planned_critical_path_minutes"`
}

type Burn struct {
	EURPerHour   float64  `json:"eur_per_hour"`
	TasksPerHour float64  `json:"tasks_per_hour"`
	RunwayHours  *float64 `json:"runway_hours"`
	Source       string   `json:"source"`
}

type Risk struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Forecast struct {
	BriefID     string    `json:"brief_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Runs        int       `json:"runs"`
	Seed        uint64    `json:"seed"`
	Tasks       Counts    `json:"tasks"`
	Cost        Cost      `json:"cost_eur"`
	Schedule    Schedule  `json:"schedule"`
	Burn        Burn      `json:"burn"`
	Risks       []Risk    `json:"risks"`
}

func (f Forecast) CostOverrun() bool {
	return f.Cost.Budget > 0 && f.Cost.Projected.P50 > f.Cost.Budget
}

type node struct {
	task     *model.Task
	deps     []int
	open     bool
	review   bool
	usedMin  float64
	usedCost float64
	expMin   float64
	expCost  float64
	sigmaMin float64
	sigmaEUR float64
}

func Build(s Snapshot, o Options) Forecast {
	runs := o.Runs
	if runs <= 0 {
		runs = defaultRuns
	}
	runs = min(runs, maxRuns)
	seed := o.Seed
	if seed == 0 {
		h := fnv.New64a()
		_, _ = h.Write([]byte(s.Brief.ID))
		seed = h.Sum64()
	}
	nodes, order := graph(s.Tasks)
	prior := fit(nodes)
	for _, n := range nodes {
		prior.apply(n)
	}

	f := Forecast{BriefID: s.Brief.ID, GeneratedAt: s.Now, Runs: runs, Seed: seed, Risks: []Risk{}}
	f.Tasks.Total = len(nodes)
	for _, n := range nodes {
		if n.open {
			f.Tasks.Remaining++
		} else {
			f.Tasks.Complete++
		}
	}

	slots := s.Brief.Budget.MaxParallelAgent
	if slots <= 0 || slots > f.Tasks.Remaining {
		slots = max(f.Tasks.Remaining, 1)
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15)) //nolint:gosec // simulation, not security
	costs := make([]float64, runs)
	spans := make([]float64, runs)
	paths := make([]float64, runs)
	dur := make([]float64, len(nodes))
	fin := make([]float64, len(nodes))
	cp := make([]float64, len(nodes))
	free := make([]float64, slots)
	budget := s.Brief.Budget.MaxCostEUR
	exceed := 0
	for r := range runs {
		var remCost float64
		for _, i := range order {
			n := nodes[i]
			dur[i] = 0
			switch {
			case n.open:
				z := rng.NormFloat64()
				scale := 1 - float64(n.task.ProgressPct)/100
				dur[i] = max(lognormal(n.expMin, n.sigmaMin*scale, z), n.usedMin) - n.usedMin
				remCost += max(lognormal(n.expCost, n.sigmaEUR*scale, z), n.usedCost) - n.usedCost
			case n.review:
				dur[i] = reviewMinutes
			}
		}
		clear(free)
		var span, path float64
		for _, i := range order {
			n := nodes[i]
			var start, cpStart float64
			for _, d := range n.deps {
				start = max(start, fin[d])
				cpStart = max(cpStart, cp[d])
			}
			if n.open {
				k := slices.Index(free, slices.Min(free))
				start = max(start, free[k])
				free[k] = start + dur[i]
			}
			fin[i] = start + dur[i]
			cp[i] = cpStart + dur[i]
			span = max(span, fin[i])
			path = max(path, cp[i])
		}
		costs[r] = s.Brief.CostUsed + remCost
		spans[r], paths[r] = span, path
		if budget > 0 && costs[r] > budget {
			exceed++
		}
	}

	f.Cost = Cost{Used: s.Brief.CostUsed, Budget: budget, Projected: quantiles(costs)}
	f.Cost.ExceedProbability = float64(exceed) / float64(runs)
	f.Schedule.RemainingMinutes = quantiles(spans)
	f.Schedule.CriticalPathMinutes = quantiles(paths)
	f.Schedule.CompletesAt = TimeRange{
		P10: s.Now.Add(minutes(f.Schedule.RemainingMinutes.P10)),
		P50: s.Now.Add(minutes(f.Schedule.RemainingMinutes.P50)),
		P90: s.Now.Add(minutes(f.Schedule.RemainingMinutes.P90)),
	}
	planned, ok := plannedPath(nodes, order)
	if ok {
		f.Schedule.PlannedCriticalPath = planned
	}
	f.Burn = burn(s, nodes, o.EURToUSD)
	f.Risks = risks(f, ok)
	return f
}

func graph(tasks []*model.Task) ([]*node, []int) {
	live := make([]*model.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status != model.StatusCancelled {
			live = append(live, t)
		}
	}
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i].ID, live[j].ID
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	index := make(map[string]int, len(live))
	nodes := make([]*node, len(live))
	for i, t := range live {
		index[t.ID] = i
		n := &node{task: t, usedMin: float64(t.MinutesUsed), usedCost: t.CostUsed}
		switch t.Status {
		case model.StatusDone, model.StatusApproved:
		case model.StatusSubmitted, model.StatusInReview:
			n.review = true
		default:
			n.open = true
		}
		nodes[i] = n
	}
	indeg := make([]int, len(nodes))
	children := make([][]int, len(nodes))
	for i, n := range nodes {
		for _, d := range n.task.DependsOn {
			if j, ok := index[d]; ok && j != i {
				n.deps = append(n.deps, j)
				children[j] = append(children[j], i)
				indeg[i]++
			}
		}
	}
	order := make([]int, 0, len(nodes))
	queue := make([]int, 0, len(nodes))
	for i := range nodes {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		order = append(order, i)
		for _, c := range children[i] {
			if indeg[c]--; indeg[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	if len(order) < len(nodes) {
		for i, n := range nodes {
			if indeg[i] > 0 {
				n.deps = nil
				order = append(order, i)
			}
		}
	}
	return nodes, order
}

type priors struct {
	minutes, cost         float64
	sigmaMin, sigmaEUR    float64
	costPerMinute         float64
	haveMinutes, haveCost bool
}

func fit(nodes []*node) priors {
	var p priors
	var mins, costs []float64
	var totalMin, totalCost float64
	for _, n := range nodes {
		totalMin += n.usedMin
		totalCost += n.usedCost
		if n.open {
			continue
		}
		if n.usedMin > 0 {
			mins = append(mins, n.usedMin)
		}
		if n.usedCost > 0 {
			costs = append(costs, n.usedCost)
		}
	}
	p.minutes, p.sigmaMin = mean(mins), logSigma(mins)
	p.cost, p.sigmaEUR = mean(costs), logSigma(costs)
	p.haveMinutes, p.haveCost = len(mins) > 0, len(costs) > 0
	if totalMin > 0 {
		p.costPerMinute = totalCost / totalMin
	}
	return p
}

func (p priors) apply(n *node) {
	if !n.open {
		return
	}
	t := n.task
	pct := float64(t.ProgressPct) / 100
	switch {
	case pct > 0 && n.usedMin > 0:
		n.expMin = n.usedMin / pct
	case p.haveMinutes:
		n.expMin = p.minutes
	case t.Budget.MaxMinutes > 0:
		n.expMin = budgetFraction * float64(t.Budget.MaxMinutes)
	default:
		n.expMin = defaultMinutes
	}
	n.expMin = max(n.expMin, n.usedMin)
	switch {
	case pct > 0 && n.usedCost > 0:
		n.expCost = n.usedCost / pct
	case p.haveCost:
		n.expCost = p.cost
	case p.costPerMinute > 0:
		n.expCost = p.costPerMinute * n.expMin
	case t.Budget.MaxCostEUR > 0:
		n.expCost = budgetFraction * t.Budget.MaxCostEUR
	}
	n.expCost = max(n.expCost, n.usedCost)
	n.sigmaMin, n.sigmaEUR = p.sigmaMin, p.sigmaEUR
}

func plannedPath(nodes []*node, order []int) (float64, bool) {
	finish := make([]float64, len(nodes))
	var longest float64
	for _, i := range order {
		n := nodes[i]
		var start float64
		for _, d := range n.deps {
			start = max(start, finish[d])
		}
		var d float64
		if n.open {
			if n.task.Budget.MaxMinutes <= 0 {
				return 0, false
			}
			d = max(float64(n.task.Budget.MaxMinutes)-n.usedMin, 0)
		}
		finish[i] = start + d
		longest = max(longest, finish[i])
	}
	return longest, true
}

func burn(s Snapshot, nodes []*node, eurToUSD float64) Burn {
	if eurToUSD <= 0 {
		eurToUSD = 1
	}
	var b Burn
	var first time.Time
	var complete int
	for _, n := range nodes {
		if c := n.task.ClaimedAt; c != nil && (first.IsZero() || c.Before(first)) {
			first = *c
		}
		if n.task.Status == model.StatusDone || n.task.Status == model.StatusApproved {
			complete++
		}
	}
	if !first.IsZero() {
		hours := max(s.Now.Sub(first).Hours(), minBurnHours)
		b.TasksPerHour = float64(complete) / hours
		b.EURPerHour = s.Brief.CostUsed / hours
		b.Source = "tasks"
	}
	since := s.Now.Add(-burnWindow)
	earliest := s.Now
	var usd float64
	for _, r := range s.Usage {
		if r.TS.Before(since) || r.TS.After(s.Now) {
			continue
		}
		usd += r.CostUSD
		if r.TS.Before(earliest) {
			earliest = r.TS
		}
	}
	if usd > 0 {
		hours := max(s.Now.Sub(earliest).Hours(), minBurnHours)
		b.EURPerHour = usd / eurToUSD / hours
		b.Source = "usage"
	}
	if budget := s.Brief.Budget.MaxCostEUR; budget > 0 && b.EURPerHour > 0 {
		h := max(budget-s.Brief.CostUsed, 0) / b.EURPerHour
		b.RunwayHours = &h
	}
	return b
}

func risks(f Forecast, planned bool) []Risk {
	out := []Risk{}
	if f.Cost.Budget > 0 {
		switch {
		case f.Cost.Used > f.Cost.Budget:
			out = append(out, Risk{CodeCostExceeded, SeverityHigh, "cost already exceeds max_cost_eur"})
		case f.Cost.ExceedProbability >= overrunHigh:
			out = append(out, Risk{CodeCostOverrun, SeverityHigh, "projected cost is likely to exceed max_cost_eur"})
		case f.Cost.ExceedProbability >= overrunMedium:
			out = append(out, Risk{CodeCostOverrun, SeverityMedium, "projected cost may exceed max_cost_eur"})
		}
	}
	if planned && f.Schedule.CriticalPathMinutes.P50 > f.Schedule.PlannedCriticalPath {
		sev := SeverityMedium
		if f.Schedule.CriticalPathMinutes.P10 > f.Schedule.PlannedCriticalPath {
			sev = SeverityHigh
		}
		out = append(out, Risk{CodePathSlip, sev, "critical path is projected to exceed the planned task budgets"})
	}
	return out
}

func lognormal(mu, sigma, z float64) float64 {
	if mu <= 0 {
		return 0
	}
	return mu * math.Exp(sigma*z-sigma*sigma/2)
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func logSigma(xs []float64) float64 {
	if len(xs) < 2 {
		return defaultSigma
	}
	logs := make([]float64, len(xs))
	for i, x := range xs {
		logs[i] = math.Log(x)
	}
	m := mean(logs)
	var ss float64
	for _, l := range logs {
		ss += (l - m) * (l - m)
	}
	return min(max(math.Sqrt(ss/float64(len(xs)-1)), minSigma), maxSigma)
}

func quantiles(xs []float64) Range {
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	at := func(q float64) float64 { return sorted[int(math.Round(q*float64(len(sorted)-1)))] }
	return Range{P10: at(0.1), P50: at(0.5), P90: at(0.9)}
}

func minutes(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }
