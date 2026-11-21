package estimate

import (
	"math"
	"slices"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	DefaultMinSamples  = 5
	DefaultPriorWeight = 3.0

	ConfidenceNone   = "none"
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"

	quantile = 0.8
)

type Options struct {
	MinSamples  int
	PriorWeight float64
}

func (o Options) withDefaults() Options {
	if o.MinSamples <= 0 {
		o.MinSamples = DefaultMinSamples
	}
	if o.PriorWeight <= 0 {
		o.PriorWeight = DefaultPriorWeight
	}
	return o
}

type Sample struct {
	TaskID  string
	Role    string
	Agent   string
	Repo    string
	Size    string
	Minutes float64
	Cost    float64
	Tokens  float64
	Rounds  float64
}

type Key struct {
	Role  string
	Agent string
	Repo  string
	Size  string
}

func KeyOf(t *model.Task, agent *model.Agent) Key {
	k := Key{Role: t.Role, Repo: t.Repo, Size: FeaturesOf(t).Size()}
	if agent != nil {
		k.Agent = AgentGroup(agent)
	}
	return k
}

func AgentGroup(a *model.Agent) string {
	return a.Kind + "/" + a.Model
}

type Stat struct {
	Median float64 `json:"median"`
	P80    float64 `json:"p80"`
}

type Level struct {
	Group   string `json:"group"`
	Key     string `json:"key"`
	Samples int    `json:"samples"`
}

type Estimate struct {
	Sufficient   bool    `json:"sufficient"`
	Confidence   string  `json:"confidence"`
	Minutes      Stat    `json:"minutes"`
	CostEUR      Stat    `json:"cost_eur"`
	Tokens       Stat    `json:"tokens"`
	ReviewRounds Stat    `json:"review_rounds"`
	Basis        []Level `json:"basis"`
}

type Model struct {
	samples []Sample
	opt     Options
}

func Fit(samples []Sample, opt Options) *Model {
	s := slices.Clone(samples)
	slices.SortFunc(s, func(a, b Sample) int { return compareIDs(a.TaskID, b.TaskID) })
	return &Model{samples: s, opt: opt.withDefaults()}
}

func (m *Model) Samples() int { return len(m.samples) }

func (m *Model) Sufficient() bool { return len(m.samples) >= m.opt.MinSamples }

type metric struct {
	value    func(Sample) float64
	positive bool
	out      func(*Estimate) *Stat
}

var metrics = []metric{
	{func(s Sample) float64 { return s.Minutes }, true, func(e *Estimate) *Stat { return &e.Minutes }},
	{func(s Sample) float64 { return s.Cost }, true, func(e *Estimate) *Stat { return &e.CostEUR }},
	{func(s Sample) float64 { return s.Tokens }, true, func(e *Estimate) *Stat { return &e.Tokens }},
	{func(s Sample) float64 { return s.Rounds }, false, func(e *Estimate) *Stat { return &e.ReviewRounds }},
}

type step struct {
	group, key string
	match      func(Sample) bool
}

func steps(k Key) []step {
	out := []step{
		{"role", k.Role, func(s Sample) bool { return s.Role == k.Role }},
		{"size", k.Size, func(s Sample) bool { return s.Size == k.Size }},
	}
	if k.Agent != "" {
		out = append(out, step{"agent", k.Agent, func(s Sample) bool { return s.Agent == k.Agent }})
	}
	if k.Repo != "" {
		out = append(out, step{"repo", k.Repo, func(s Sample) bool { return s.Repo == k.Repo }})
	}
	return out
}

func (m *Model) Estimate(k Key) Estimate {
	e := Estimate{Confidence: ConfidenceNone, Basis: []Level{}}
	if !m.Sufficient() {
		return e
	}
	e.Sufficient = true
	pools := [][]Sample{m.samples}
	for _, st := range steps(k) {
		pool := filter(pools[len(pools)-1], st.match)
		if len(pool) == 0 {
			break
		}
		if len(pool) < len(pools[len(pools)-1]) {
			pools = append(pools, pool)
		}
		e.Basis = append(e.Basis, Level{Group: st.group, Key: st.key, Samples: len(pool)})
	}
	for _, mt := range metrics {
		*mt.out(&e) = m.shrink(pools, mt)
	}
	e.Confidence = confidence(len(pools[len(pools)-1]), len(e.Basis))
	return e
}

func (m *Model) shrink(pools [][]Sample, mt metric) Stat {
	var out Stat
	for i, pool := range pools {
		values := collect(pool, mt)
		if len(values) == 0 {
			if i == 0 {
				return Stat{}
			}
			continue
		}
		med, p80 := percentile(values, 0.5), percentile(values, quantile)
		if i == 0 {
			out = Stat{Median: med, P80: p80}
			continue
		}
		w := float64(len(values)) / (float64(len(values)) + m.opt.PriorWeight)
		out.Median = w*med + (1-w)*out.Median
		out.P80 = max(w*p80+(1-w)*out.P80, out.Median)
	}
	return Stat{Median: round2(out.Median), P80: round2(out.P80)}
}

func confidence(deepest, levels int) string {
	switch {
	case levels == 0 || deepest < 3:
		return ConfidenceLow
	case deepest < 10:
		return ConfidenceMedium
	}
	return ConfidenceHigh
}

func filter(in []Sample, keep func(Sample) bool) []Sample {
	var out []Sample
	for _, s := range in {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func collect(pool []Sample, mt metric) []float64 {
	values := make([]float64, 0, len(pool))
	for _, s := range pool {
		if v := mt.value(s); v > 0 || (!mt.positive && v >= 0) {
			values = append(values, v)
		}
	}
	slices.Sort(values)
	return values
}

func percentile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func compareIDs(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
