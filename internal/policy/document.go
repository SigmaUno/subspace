package policy

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	EffectDeny        = "deny"
	EffectApproval    = "require_owner_approval"
	EffectWarn        = "warn"
	EffectMaxParallel = "set_max_parallel"
	EffectRoute       = "route_to_provider"

	PerAgent    = "agent"
	PerProvider = "provider"
	PerBrief    = "brief"

	MaxRules        = 50
	MaxPolicies     = 100
	MaxNameLength   = 100
	MaxMessage      = 500
	DefaultTimeout  = 24 * time.Hour
	MaxTimeout      = 30 * 24 * time.Hour
	MinTimeout      = time.Minute
	maxDescription  = 2000
	maxParallelSize = 10000
)

var Effects = []string{EffectDeny, EffectApproval, EffectWarn, EffectMaxParallel, EffectRoute}

var ruleIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type When struct {
	Actions []string `json:"actions"`
	Match   string   `json:"match"`
}

type Then struct {
	Effect   string `json:"effect"`
	Message  string `json:"message,omitempty"`
	Provider string `json:"provider,omitempty"`
	Max      int    `json:"max,omitempty"`
	Per      string `json:"per,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

type Rule struct {
	ID   string `json:"id"`
	When When   `json:"when"`
	Then Then   `json:"then"`
}

type Document struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Timezone    string `json:"timezone"`
	Rules       []Rule `json:"rules"`
}

type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Rule     string `json:"rule,omitempty"`
	Message  string `json:"message"`
}

const (
	SevError   = "error"
	SevWarning = "warning"
)

type compiledRule struct {
	Rule
	expr    *Expr
	actions map[string]bool
	timeout time.Duration
}

type Compiled struct {
	ID      string
	Version int
	Doc     Document
	loc     *time.Location
	rules   []*compiledRule
}

func (d *Document) normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Timezone = strings.TrimSpace(d.Timezone)
	if d.Timezone == "" {
		d.Timezone = "UTC"
	}
	if d.Rules == nil {
		d.Rules = []Rule{}
	}
	for i := range d.Rules {
		r := &d.Rules[i]
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			r.ID = fmt.Sprintf("rule-%d", i+1)
		}
		if r.When.Actions == nil {
			r.When.Actions = []string{}
		}
	}
}

func Lint(d Document) []Finding {
	_, fs := compile("", 0, d)
	return fs
}

func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}

func compile(id string, version int, d Document) (*Compiled, []Finding) {
	d.normalize()
	fs := []Finding{}
	add := func(sev, code, rule, format string, a ...any) {
		fs = append(fs, Finding{Severity: sev, Code: code, Rule: rule, Message: fmt.Sprintf(format, a...)})
	}
	switch {
	case d.Name == "":
		add(SevError, "invalid_name", "", "name is required")
	case utf8.RuneCountInString(d.Name) > MaxNameLength:
		add(SevError, "invalid_name", "", "name is longer than %d characters", MaxNameLength)
	case strings.ContainsFunc(d.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		add(SevError, "invalid_name", "", "name contains control characters")
	}
	if len(d.Description) > maxDescription {
		add(SevError, "invalid_description", "", "description is longer than %d bytes", maxDescription)
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil || strings.EqualFold(d.Timezone, "local") {
		add(SevError, "invalid_timezone", "", "unknown timezone %q", d.Timezone)
		loc = time.UTC
	}
	if len(d.Rules) == 0 {
		add(SevWarning, "no_rules", "", "the policy has no rules")
	}
	if len(d.Rules) > MaxRules {
		add(SevError, "too_many_rules", "", "at most %d rules per policy", MaxRules)
		d.Rules = d.Rules[:MaxRules]
	}
	c := &Compiled{ID: id, Version: version, Doc: d, loc: loc}
	seen := map[string]bool{}
	for i := range d.Rules {
		r := d.Rules[i]
		cr := &compiledRule{Rule: r, actions: map[string]bool{}}
		if !ruleIDRe.MatchString(r.ID) {
			add(SevError, "invalid_rule_id", r.ID, "rule id must match %s", ruleIDRe)
		}
		if seen[r.ID] {
			add(SevError, "duplicate_rule", r.ID, "rule id %q is used twice", r.ID)
		}
		seen[r.ID] = true
		if len(r.When.Actions) == 0 {
			add(SevError, "no_actions", r.ID, "when.actions must name at least one action")
		}
		for _, a := range r.When.Actions {
			if !containsStr(Actions, a) {
				add(SevError, "unknown_action", r.ID, "unknown action %q (want one of %s)", a, strings.Join(Actions, ", "))
				continue
			}
			cr.actions[a] = true
		}
		lintEffect(cr, add)
		e, err := Compile(r.When.Match, lookupFact)
		if err != nil {
			for _, f := range UnknownFacts(r.When.Match, lookupFact) {
				add(SevError, "unknown_fact", r.ID, "unknown fact %q", f)
			}
			add(SevError, "invalid_match", r.ID, "match: %v", err)
			c.rules = append(c.rules, cr)
			continue
		}
		cr.expr = e
		lintFacts(cr, add)
		c.rules = append(c.rules, cr)
	}
	lintReachability(c.rules, add)
	if HasErrors(fs) {
		return nil, fs
	}
	return c, fs
}

func lintEffect(cr *compiledRule, add func(sev, code, rule, format string, a ...any)) {
	t := &cr.Then
	id := cr.ID
	if utf8.RuneCountInString(t.Message) > MaxMessage {
		add(SevError, "invalid_message", id, "message is longer than %d characters", MaxMessage)
	}
	bad := func(field string) {
		add(SevError, "invalid_effect", id, "%s is only valid for %s", field, map[string]string{
			"provider": EffectRoute, "max": EffectMaxParallel, "per": EffectMaxParallel, "timeout": EffectApproval}[field])
	}
	if t.Provider != "" && t.Effect != EffectRoute {
		bad("provider")
	}
	if t.Max != 0 && t.Effect != EffectMaxParallel {
		bad("max")
	}
	if t.Per != "" && t.Effect != EffectMaxParallel {
		bad("per")
	}
	if t.Timeout != "" && t.Effect != EffectApproval {
		bad("timeout")
	}
	switch t.Effect {
	case EffectDeny, EffectWarn:
	case EffectRoute:
		if strings.TrimSpace(t.Provider) == "" {
			add(SevError, "invalid_effect", id, "route_to_provider needs provider")
		}
	case EffectMaxParallel:
		if t.Max < 1 || t.Max > maxParallelSize {
			add(SevError, "invalid_effect", id, "set_max_parallel needs max between 1 and %d", maxParallelSize)
		}
		if t.Per == "" {
			t.Per = PerAgent
		}
		if t.Per != PerAgent && t.Per != PerProvider && t.Per != PerBrief {
			add(SevError, "invalid_effect", id, "per must be agent, provider or brief")
		}
	case EffectApproval:
		cr.timeout = DefaultTimeout
		if t.Timeout != "" {
			d, err := time.ParseDuration(t.Timeout)
			if err != nil || d < MinTimeout || d > MaxTimeout {
				add(SevError, "invalid_effect", id, "timeout must be a duration between %s and %s", MinTimeout, MaxTimeout)
			}
			cr.timeout = d
		}
	default:
		add(SevError, "unknown_effect", id, "unknown effect %q (want one of %s)", t.Effect, strings.Join(Effects, ", "))
	}
}

func lintFacts(cr *compiledRule, add func(sev, code, rule, format string, a ...any)) {
	for _, name := range cr.expr.Facts {
		info := factIndex[name]
		var missing []string
		for _, a := range cr.When.Actions {
			if cr.actions[a] && !containsStr(info.Actions, a) {
				missing = append(missing, a)
			}
		}
		if len(missing) > 0 {
			add(SevWarning, "fact_unavailable", cr.ID, "fact %q is not set for %s and reads as its zero value", name, strings.Join(missing, ", "))
		}
	}
}

func lintReachability(rules []*compiledRule, add func(sev, code, rule, format string, a ...any)) {
	for i, r := range rules {
		if r.expr == nil {
			continue
		}
		v, ok := r.expr.Constant()
		switch {
		case ok && !v:
			add(SevWarning, "unreachable", r.ID, "match is always false; the rule never applies")
			continue
		case ok && v && r.expr.root != nil:
			add(SevWarning, "always_true", r.ID, "match is always true; the rule applies to every %s", strings.Join(r.When.Actions, ", "))
		}
		for _, prev := range rules[:i] {
			if prev.expr == nil || prev.Then.Effect != EffectDeny || len(prev.actions) == 0 {
				continue
			}
			if pv, pok := prev.expr.Constant(); !pok || !pv {
				continue
			}
			covered := len(r.actions) > 0
			for a := range r.actions {
				if !prev.actions[a] {
					covered = false
				}
			}
			if covered {
				add(SevWarning, "unreachable", r.ID, "rule %q always denies these actions first; this rule never takes effect", prev.ID)
				break
			}
		}
	}
}
