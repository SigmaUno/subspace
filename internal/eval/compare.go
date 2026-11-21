package eval

import (
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"text/tabwriter"
)

type Comparison struct {
	Suite       string  `json:"suite"`
	Pairs       int     `json:"pairs"`
	MeanA       float64 `json:"mean_a"`
	MeanB       float64 `json:"mean_b"`
	Delta       float64 `json:"delta"`
	Low         float64 `json:"ci_low"`
	High        float64 `json:"ci_high"`
	Significant bool    `json:"significant"`
}

func Compare(a, b *Report, seed uint64, resamples int) []Comparison {
	scores := func(r *Report) map[string]Case {
		out := map[string]Case{}
		for _, c := range r.Cases {
			if c.scored() {
				out[c.Suite+"/"+c.ID] = c
			}
		}
		return out
	}
	sa, sb := scores(a), scores(b)
	var out []Comparison
	for _, suite := range []string{SuitePlan, SuiteReview, SuiteRobustness} {
		var xs, ys []float64
		for _, c := range a.Cases {
			ca, ok1 := sa[suite+"/"+c.ID]
			cb, ok2 := sb[suite+"/"+c.ID]
			if c.Suite == suite && ok1 && ok2 {
				xs = append(xs, ca.Score)
				ys = append(ys, cb.Score)
			}
		}
		out = append(out, comparePaired(suite, xs, ys, seed, resamples))
	}
	return out
}

func comparePaired(suite string, a, b []float64, seed uint64, resamples int) Comparison {
	c := Comparison{Suite: suite, Pairs: len(a)}
	if len(a) == 0 {
		return c
	}
	d := make([]float64, len(a))
	for i := range a {
		d[i] = b[i] - a[i]
	}
	c.MeanA, c.MeanB, c.Delta = mean(a), mean(b), mean(d)
	c.Low, c.High = bootstrap(d, seed, resamples)
	c.Significant = c.Low > 0 || c.High < 0
	return c
}

func bootstrap(d []float64, seed uint64, resamples int) (float64, float64) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // reproducible resampling, not security
	resamples = max(resamples, 1)
	means := make([]float64, resamples)
	for i := range means {
		var s float64
		for range d {
			s += d[rng.IntN(len(d))]
		}
		means[i] = s / float64(len(d))
	}
	slices.Sort(means)
	at := func(q float64) float64 {
		return means[min(int(q*float64(resamples)), resamples-1)]
	}
	return at(0.025), at(0.975)
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

type table struct {
	tw  *tabwriter.Writer
	err error
}

func newTable(w io.Writer) *table {
	return &table{tw: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
}

func (t *table) printf(format string, a ...any) {
	if t.err == nil {
		_, t.err = fmt.Fprintf(t.tw, format, a...)
	}
}

func (t *table) flush() error {
	if t.err != nil {
		return t.err
	}
	return t.tw.Flush()
}

func WriteTable(w io.Writer, r *Report, verbose bool) error {
	t := newTable(w)
	t.printf("model\t%s\nplan prompt\t%s\nreview prompt\t%s\n\n", r.Model, r.PlanPrompt, r.ReviewPrompt)
	t.printf("suite\tcases\tscored\tskipped\terrors\tunsafe\tmean\tmin\n")
	for _, s := range r.Summaries {
		t.printf("%s\t%d\t%d\t%d\t%d\t%d\t%.3f\t%.3f\n", s.Suite, s.Cases, s.Scored, s.Skipped, s.Errors, s.Unsafe, s.Mean, s.Min)
	}
	t.printf("\nrequests\t%d\ntokens in/out\t%d/%d\ncost\t%.4f EUR\n", r.Usage.Requests, r.Usage.TokensIn, r.Usage.TokensOut, r.Usage.CostEUR)
	header := false
	for _, c := range r.Cases {
		if !verbose && c.scored() && c.Score >= 1 {
			continue
		}
		if !header {
			t.printf("\nsuite\tcase\tscore\tnotes\n")
			header = true
		}
		detail := c.Error
		if detail == "" {
			detail = strings.Join(c.Notes, "; ")
		}
		t.printf("%s\t%s\t%.3f\t%s\n", c.Suite, c.ID, c.Score, detail)
	}
	return t.flush()
}

func WriteComparison(w io.Writer, cs []Comparison) error {
	t := newTable(w)
	t.printf("suite\tpairs\tmean a\tmean b\tdelta\t95%% CI\tsignificant\n")
	for _, c := range cs {
		t.printf("%s\t%d\t%.3f\t%.3f\t%+.3f\t[%+.3f, %+.3f]\t%t\n", c.Suite, c.Pairs, c.MeanA, c.MeanB, c.Delta, c.Low, c.High, c.Significant)
	}
	return t.flush()
}

type Floors struct {
	Plan       float64
	Review     float64
	Robustness float64
	MaxUnsafe  int
}

func (r *Report) Check(f Floors) []string {
	var out []string
	for _, s := range r.Summaries {
		floor := map[string]float64{SuitePlan: f.Plan, SuiteReview: f.Review, SuiteRobustness: f.Robustness}[s.Suite]
		if s.Mean < floor {
			out = append(out, fmt.Sprintf("%s mean %.3f is below the floor %.3f", s.Suite, s.Mean, floor))
		}
		if s.Unsafe > f.MaxUnsafe {
			out = append(out, fmt.Sprintf("%s has %d unsafe approvals, at most %d allowed", s.Suite, s.Unsafe, f.MaxUnsafe))
		}
		if floor > 0 && s.Errors > 0 {
			out = append(out, fmt.Sprintf("%s has %d errored cases", s.Suite, s.Errors))
		}
	}
	return out
}
