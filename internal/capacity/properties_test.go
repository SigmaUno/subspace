package capacity

import (
	"math/rand"
	"testing"
)

const seeds = 2000

var (
	providers = []string{"anthropic", "openai", "deepseek"}
	models    = []string{"m1", "m2", "m3"}
)

func pick(r *rand.Rand, xs []string, wildcard bool) string {
	if wildcard && r.Intn(3) == 0 {
		return "*"
	}
	return xs[r.Intn(len(xs))]
}

func randomConfig(r *rand.Rand) Config {
	c := Config{GlobalMaxAgents: r.Intn(12)}
	for i := r.Intn(5); i > 0; i-- {
		c.Limits = append(c.Limits, Limit{
			Provider:       pick(r, providers, true),
			Model:          pick(r, models, true),
			MaxConcurrency: r.Intn(8),
			RequestsPerMin: r.Intn(100),
		})
	}
	return c
}

func randomUsage(r *rand.Rand) Usage {
	u := Usage{}
	for _, p := range providers {
		for _, m := range models {
			if r.Intn(2) == 0 {
				u[[2]string{p, m}] = r.Intn(5)
			}
		}
	}
	return u
}

func usedBy(u Usage, provider, model string) int {
	n := 0
	for k, c := range u {
		if (provider == "*" || provider == k[0]) && (model == "*" || model == k[1]) {
			n += c
		}
	}
	return n
}

func TestCheckAllowsExactlyWhatFits(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		c, u := randomConfig(r), randomUsage(r)
		provider, model, n := pick(r, providers, false), pick(r, models, false), 1+r.Intn(3)

		fits := c.GlobalMaxAgents == 0 || usedBy(u, "*", "*")+n <= c.GlobalMaxAgents
		for _, l := range c.Limits {
			matches := (l.Provider == "*" || l.Provider == provider) && (l.Model == "*" || l.Model == model)
			if matches && l.MaxConcurrency > 0 && usedBy(u, l.Provider, l.Model)+n > l.MaxConcurrency {
				fits = false
			}
		}
		if err := c.Check(provider, model, n, u); (err == nil) != fits {
			t.Fatalf("seed %d: Check(%s/%s, %d) = %v, want fits=%v\nconfig %+v\nusage %v", seed, provider, model, n, err, fits, c, u)
		}
	}
}

func TestCheckNeverExceedsAfterAdmission(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		c := randomConfig(r)
		u := Usage{}
		for i := 0; i < 60; i++ {
			provider, model := pick(r, providers, false), pick(r, models, false)
			if c.Check(provider, model, 1, u) == nil {
				u[[2]string{provider, model}]++
			}
		}
		if c.GlobalMaxAgents > 0 && usedBy(u, "*", "*") > c.GlobalMaxAgents {
			t.Fatalf("seed %d: %d agents admitted over global max %d", seed, usedBy(u, "*", "*"), c.GlobalMaxAgents)
		}
		for _, l := range c.Limits {
			if l.MaxConcurrency > 0 && usedBy(u, l.Provider, l.Model) > l.MaxConcurrency {
				t.Fatalf("seed %d: limit %+v exceeded: %d in use", seed, l, usedBy(u, l.Provider, l.Model))
			}
		}
	}
}

func TestGlobalLimitEqualsWildcardLimit(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		max := 1 + r.Intn(10)
		global := Config{GlobalMaxAgents: max}
		wildcard := Config{Limits: []Limit{{Provider: "*", Model: "*", MaxConcurrency: max}}}
		u := randomUsage(r)
		provider, model, n := pick(r, providers, false), pick(r, models, false), 1+r.Intn(3)
		if a, b := global.Check(provider, model, n, u), wildcard.Check(provider, model, n, u); (a == nil) != (b == nil) {
			t.Fatalf("seed %d: global=%v wildcard=%v for max %d n %d usage %v", seed, a, b, max, n, u)
		}
	}
}

func TestReportAgreesWithCheck(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		c, u := randomConfig(r), randomUsage(r)
		c.GlobalMaxAgents = 0
		reports := c.Report(u)
		if len(reports) != len(c.Limits) {
			t.Fatalf("seed %d: %d reports for %d limits", seed, len(reports), len(c.Limits))
		}
		for i, rep := range reports {
			switch {
			case rep.MaxConcurrency == 0 && rep.Available != -1:
				t.Fatalf("seed %d: unlimited %+v reports available %d", seed, rep, rep.Available)
			case rep.MaxConcurrency > 0 && rep.Available < 0:
				t.Fatalf("seed %d: %+v reports negative available %d", seed, rep, rep.Available)
			case rep.MaxConcurrency > 0 && rep.InUse < rep.MaxConcurrency && rep.InUse+rep.Available != rep.MaxConcurrency:
				t.Fatalf("seed %d: limit %d in_use %d + available %d != max %d", seed, i, rep.InUse, rep.Available, rep.MaxConcurrency)
			}
		}

		provider, model := pick(r, providers, false), pick(r, models, false)
		open := true
		for _, rep := range reports {
			matches := (rep.Provider == "*" || rep.Provider == provider) && (rep.Model == "*" || rep.Model == model)
			if matches && rep.Available == 0 {
				open = false
			}
		}
		if err := c.Check(provider, model, 1, u); (err == nil) != open {
			t.Fatalf("seed %d: Check(%s/%s)=%v but reports say open=%v: %+v", seed, provider, model, err, open, reports)
		}
	}
}
