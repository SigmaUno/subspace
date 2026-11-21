package briefs

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
)

const defaultParallel = 10

type TaskBudget struct {
	MaxMinutes int     `json:"max_minutes"`
	MaxCostEUR float64 `json:"max_cost_eur"`
}

type Doc struct {
	Title            string            `json:"title"`
	Body             string            `json:"body"`
	Repos            []string          `json:"repos"`
	MaxCostEUR       float64           `json:"max_cost_eur"`
	MaxParallel      int               `json:"max_parallel_agents"`
	AllowedProviders []string          `json:"allowed_providers"`
	CrossReview      bool              `json:"cross_review"`
	TaskBudget       *TaskBudget       `json:"task_budget"`
	Components       []components.Spec `json:"components"`
}

func Parse(src []byte) (*Doc, error) {
	lines, body, err := splitDocument(src)
	if err != nil {
		return nil, err
	}
	root, err := parseFrontmatter(lines)
	if err != nil {
		return nil, err
	}
	d := &Doc{Body: body, Repos: []string{}, AllowedProviders: []string{}, CrossReview: true, Components: []components.Spec{}}
	hasTitle := false
	for _, e := range root.keys {
		v := e.val
		switch e.key {
		case "title":
			d.Title, err = text(v, e.key)
			if err == nil && strings.TrimSpace(d.Title) == "" {
				err = errAt(v.pos, "title must not be empty")
			}
			d.Title = strings.TrimSpace(d.Title)
			hasTitle = true
		case "repos":
			d.Repos, err = textList(v, e.key)
		case "max_cost_eur":
			d.MaxCostEUR, err = number(v, e.key)
		case "max_parallel_agents":
			d.MaxParallel, err = integer(v, e.key)
		case "allowed_providers":
			d.AllowedProviders, err = textList(v, e.key)
		case "cross_review":
			d.CrossReview, err = boolean(v, e.key)
		case "task_budget":
			d.TaskBudget, err = taskBudget(v)
		case "components":
			d.Components, err = componentList(v, e.pos)
		default:
			err = errAt(e.pos, "unknown key %q (allowed: title, repos, max_cost_eur, max_parallel_agents, allowed_providers, cross_review, task_budget, components)", e.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if !hasTitle {
		return nil, errAt(root.pos, "title is required")
	}
	if d.MaxParallel == 0 {
		d.MaxParallel = defaultParallel
	}
	return d, nil
}

func (d *Doc) BriefInput() board.BriefInput {
	body := d.Body
	if d.TaskBudget != nil {
		section := fmt.Sprintf("## Task budget defaults\n\n- max_minutes: %d\n- max_cost_eur: %s\n",
			d.TaskBudget.MaxMinutes, strconv.FormatFloat(d.TaskBudget.MaxCostEUR, 'f', -1, 64))
		if body != "" {
			body += "\n\n"
		}
		body += section
	}
	cross := d.CrossReview
	return board.BriefInput{Title: d.Title, Body: body, Repos: d.Repos, MaxCostEUR: d.MaxCostEUR, MaxParallel: d.MaxParallel,
		AllowedProviders: d.AllowedProviders, CrossReview: &cross}
}

func text(n *node, field string) (string, error) {
	if n.kind != scalarNode {
		return "", errAt(n.pos, "%s must be a string, not %s", field, n.describe())
	}
	return n.value, nil
}

func textList(n *node, field string) ([]string, error) {
	if n.kind != listNode {
		return nil, errAt(n.pos, "%s must be a list, not %s", field, n.describe())
	}
	out := make([]string, 0, len(n.items))
	seen := map[string]bool{}
	for _, it := range n.items {
		v, err := text(it, field+" item")
		if err != nil {
			return nil, err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errAt(it.pos, "%s item must not be empty", field)
		}
		if seen[v] {
			return nil, errAt(it.pos, "duplicate %s item %q", field, clip(v))
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

func plainScalar(n *node, field, want string) (string, error) {
	if n.kind != scalarNode || !n.plain {
		return "", errAt(n.pos, "%s must be %s", field, want)
	}
	return n.value, nil
}

func number(n *node, field string) (float64, error) {
	const want = "a non-negative number"
	s, err := plainScalar(n, field, want)
	if err != nil {
		return 0, err
	}
	f, perr := strconv.ParseFloat(s, 64)
	if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || strings.ContainsAny(s, "xXpP_") {
		return 0, errAt(n.pos, "%s must be %s, got %q", field, want, clip(s))
	}
	return f, nil
}

func integer(n *node, field string) (int, error) {
	const want = "a non-negative integer"
	s, err := plainScalar(n, field, want)
	if err != nil {
		return 0, err
	}
	i, perr := strconv.Atoi(s)
	if perr != nil || i < 0 || i > 1_000_000 || s[0] == '+' {
		return 0, errAt(n.pos, "%s must be %s, got %q", field, want, clip(s))
	}
	return i, nil
}

func boolean(n *node, field string) (bool, error) {
	s, err := plainScalar(n, field, "true or false")
	if err != nil {
		return false, err
	}
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, errAt(n.pos, "%s must be true or false, got %q", field, clip(s))
}

func mapping(n *node, field string, allowed ...string) (map[string]entry, error) {
	if n.kind != mapNode {
		return nil, errAt(n.pos, "%s must be a mapping, not %s", field, n.describe())
	}
	out := make(map[string]entry, len(n.keys))
	for _, e := range n.keys {
		known := false
		for _, a := range allowed {
			known = known || a == e.key
		}
		if !known {
			return nil, errAt(e.pos, "unknown key %q in %s (allowed: %s)", e.key, field, strings.Join(allowed, ", "))
		}
		out[e.key] = e
	}
	return out, nil
}

func taskBudget(n *node) (*TaskBudget, error) {
	m, err := mapping(n, "task_budget", "max_minutes", "max_cost_eur")
	if err != nil {
		return nil, err
	}
	var b TaskBudget
	if e, ok := m["max_minutes"]; ok {
		if b.MaxMinutes, err = integer(e.val, "task_budget.max_minutes"); err != nil {
			return nil, err
		}
	}
	if e, ok := m["max_cost_eur"]; ok {
		if b.MaxCostEUR, err = number(e.val, "task_budget.max_cost_eur"); err != nil {
			return nil, err
		}
	}
	return &b, nil
}

func componentList(n *node, at Pos) ([]components.Spec, error) {
	if n.kind != listNode {
		return nil, errAt(n.pos, "components must be a list, not %s", n.describe())
	}
	specs := make([]components.Spec, 0, len(n.items))
	for _, it := range n.items {
		m, err := mapping(it, "component", "name", "description", "paths", "depends_on")
		if err != nil {
			return nil, err
		}
		name, ok := m["name"]
		if !ok {
			return nil, errAt(it.pos, "component name is required")
		}
		sp := components.Spec{Paths: []string{}, DependsOn: []string{}}
		if sp.Name, err = text(name.val, "component name"); err != nil {
			return nil, err
		}
		if e, ok := m["description"]; ok {
			if sp.Description, err = text(e.val, "component description"); err != nil {
				return nil, err
			}
		}
		if e, ok := m["paths"]; ok {
			if sp.Paths, err = textList(e.val, "paths"); err != nil {
				return nil, err
			}
		}
		if e, ok := m["depends_on"]; ok {
			if sp.DependsOn, err = textList(e.val, "depends_on"); err != nil {
				return nil, err
			}
		}
		specs = append(specs, sp)
	}
	if err := components.Validate(specs); err != nil {
		return nil, errAt(at, "components: %s", model.AsError(err).Message)
	}
	return specs, nil
}
