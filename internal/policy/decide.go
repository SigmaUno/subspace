package policy

import (
	"fmt"
	"time"
)

const (
	OutcomeAllow    = "allow"
	OutcomeWarn     = "warn"
	OutcomeDeny     = "deny"
	OutcomeHold     = "hold"
	OutcomeApproved = "approved"
)

type Match struct {
	PolicyID string `json:"policy_id"`
	Policy   string `json:"policy"`
	Version  int    `json:"version"`
	Rule     string `json:"rule"`
	Effect   string `json:"effect"`
	Message  string `json:"message"`
	Applied  bool   `json:"applied"`
}

type Result struct {
	Outcome string         `json:"outcome"`
	Message string         `json:"message"`
	Matched []Match        `json:"matched"`
	Facts   map[string]any `json:"facts"`
	Errors  []string       `json:"errors"`
	Steps   int            `json:"steps"`

	timeout time.Duration
}

func relevant(set []*Compiled, action string) bool {
	for _, c := range set {
		if !c.Doc.Enabled {
			continue
		}
		for _, r := range c.rules {
			if r.actions[action] {
				return true
			}
		}
	}
	return false
}

func Decide(set []*Compiled, action string, base Facts, now time.Time, overrides map[string]Value) *Result {
	res := &Result{Outcome: OutcomeAllow, Matched: []Match{}, Facts: map[string]any{}, Errors: []string{}}
	deny, hold, warn := -1, -1, -1
	for _, c := range set {
		f := located{base: base, loc: c.loc, now: now, overrides: overrides}
		for _, r := range c.rules {
			if !r.actions[action] {
				continue
			}
			ok, steps, err := r.expr.Eval(f)
			res.Steps += steps
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s/%s: %v", c.Doc.Name, r.ID, err))
				continue
			}
			for _, name := range r.expr.Facts {
				res.Facts[name] = f.Fact(name).Any()
			}
			if !ok {
				continue
			}
			m := Match{PolicyID: c.ID, Policy: c.Doc.Name, Version: c.Version, Rule: r.ID, Effect: r.Then.Effect, Message: r.Then.Message, Applied: true}
			switch r.Then.Effect {
			case EffectRoute:
				got := f.Fact("subject.provider").s
				res.Facts["subject.provider"] = got
				m.Applied = got != r.Then.Provider
				if m.Message == "" {
					m.Message = fmt.Sprintf("%s must go to provider %s, not %q", action, r.Then.Provider, got)
				}
			case EffectMaxParallel:
				name := "count.in_progress_" + r.Then.Per
				n := f.Fact(name).n
				res.Facts[name] = n
				m.Applied = n >= float64(r.Then.Max)
				if m.Message == "" {
					m.Message = fmt.Sprintf("at most %d in_progress tasks per %s (now %g)", r.Then.Max, r.Then.Per, n)
				}
			case EffectApproval:
				res.timeout = max(res.timeout, r.timeout)
				if m.Message == "" {
					m.Message = fmt.Sprintf("%s needs owner approval", action)
				}
			default:
				if m.Message == "" {
					m.Message = fmt.Sprintf("%s by policy %s rule %s", r.Then.Effect, c.Doc.Name, r.ID)
				}
			}
			res.Matched = append(res.Matched, m)
			if !m.Applied {
				continue
			}
			last := len(res.Matched) - 1
			switch r.Then.Effect {
			case EffectDeny, EffectRoute, EffectMaxParallel:
				if deny < 0 {
					deny = last
				}
			case EffectApproval:
				if hold < 0 {
					hold = last
				}
			case EffectWarn:
				if warn < 0 {
					warn = last
				}
			}
		}
	}
	switch {
	case deny >= 0:
		res.Outcome, res.Message = OutcomeDeny, res.Matched[deny].Message
	case hold >= 0:
		res.Outcome, res.Message = OutcomeHold, res.Matched[hold].Message
	case warn >= 0:
		res.Outcome, res.Message = OutcomeWarn, res.Matched[warn].Message
	}
	return res
}

func (r *Result) warnings() []Match {
	var out []Match
	for _, m := range r.Matched {
		if m.Applied && m.Effect == EffectWarn {
			out = append(out, m)
		}
	}
	return out
}

func (r *Result) applied() bool {
	for _, m := range r.Matched {
		if m.Applied {
			return true
		}
	}
	return len(r.Errors) > 0
}
