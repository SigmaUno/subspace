package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
)

var validPlaybook = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var playbookFields = []string{"name", "description", "coverage", "required_tasks", "criteria_rules", "allowed_sizes",
	"default_task_budget", "brief_cost_multiplier", "cross_review", "instructions"}

var playbookCommands = table{
	"list":   plain("playbooks list", request{method: http.MethodGet, path: "playbooks"}),
	"show":   playbookByName("playbooks show", http.MethodGet, ""),
	"create": parsePlaybookWrite("playbooks create", http.MethodPost),
	"update": parsePlaybookWrite("playbooks update", http.MethodPut),
	"delete": playbookByName("playbooks delete", http.MethodDelete, ""),
	"clone":  parsePlaybookClone,
}

func playbookByName(label, method, suffix string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		name, err := playbookName(flags(label), args)
		if err != nil {
			return nil, err
		}
		return call(request{method: method, path: "playbooks/" + name + suffix}), nil
	}
}

func playbookName(set *flag.FlagSet, args []string) (string, error) {
	if err := parseFlags(set, args, 1, 1); err != nil {
		return "", err
	}
	return matching(validPlaybook, "playbook name", set.Arg(0))
}

type playbookFlags struct {
	file, description, coverage, sizes, instructions string
	crossReview                                      bool
	multiplier, maxCost                              float64
	maxMinutes                                       int
}

func (f *playbookFlags) register(set *flag.FlagSet) {
	set.StringVar(&f.file, "file", "", "full playbook JSON object or - for stdin; cannot be combined with other flags")
	set.StringVar(&f.description, "description", "", "what the playbook is for")
	set.StringVar(&f.coverage, "coverage", "", "tester coverage: empty, tester or tester_or_reviewer")
	set.StringVar(&f.sizes, "allowed-sizes", "", "comma-separated task sizes from s, m, l, xl (empty = any)")
	set.StringVar(&f.instructions, "instructions", "", "instructions for the leader")
	set.BoolVar(&f.crossReview, "cross-review", false, "require cross review on briefs")
	set.Float64Var(&f.multiplier, "brief-cost-multiplier", 0, "scale of the brief max_cost_eur at creation (0 = off)")
	set.IntVar(&f.maxMinutes, "max-minutes", 0, "default task budget minutes")
	set.Float64Var(&f.maxCost, "max-cost-eur", 0, "default task budget EUR")
}

func (f *playbookFlags) changes(changed map[string]bool) map[string]any {
	out := map[string]any{}
	budget := map[string]any{}
	if changed["description"] {
		out["description"] = f.description
	}
	if changed["coverage"] {
		out["coverage"] = f.coverage
	}
	if changed["allowed-sizes"] {
		out["allowed_sizes"] = splitList(f.sizes)
	}
	if changed["instructions"] {
		out["instructions"] = f.instructions
	}
	if changed["cross-review"] {
		out["cross_review"] = f.crossReview
	}
	if changed["brief-cost-multiplier"] {
		out["brief_cost_multiplier"] = f.multiplier
	}
	if changed["max-minutes"] {
		budget["max_minutes"] = f.maxMinutes
	}
	if changed["max-cost-eur"] {
		budget["max_cost_eur"] = f.maxCost
	}
	if len(budget) > 0 {
		out["default_task_budget"] = budget
	}
	return out
}

func parsePlaybookWrite(label, method string) parser {
	return func(args []string, stdin io.Reader) (command, error) {
		set := flags(label)
		var f playbookFlags
		f.register(set)
		name, err := playbookName(set, args)
		if err != nil {
			return nil, err
		}
		changed := visited(set)
		if changed["file"] && len(changed) > 1 {
			return nil, fmt.Errorf("%s: --file cannot be combined with other flags", label)
		}
		if method == http.MethodPut && len(changed) == 0 {
			return nil, fmt.Errorf("%s needs --file or at least one flag", label)
		}
		var full map[string]any
		if f.file != "" {
			data, err := readInput(f.file, stdin)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", label, err)
			}
			if err := json.Unmarshal(data, &full); err != nil || full == nil {
				return nil, fmt.Errorf("%s: input must be a JSON object", label)
			}
		}
		changes := f.changes(changed)
		path := "playbooks"
		if method == http.MethodPut {
			path += "/" + name
		}
		return func(ctx context.Context, c *conn, p *printer) error {
			body := full
			if body == nil {
				body = map[string]any{}
				if method == http.MethodPut {
					cur, err := currentPlaybook(ctx, c, name)
					if err != nil {
						return err
					}
					body = cur
				}
				overlay(body, changes)
			}
			body["name"] = name
			resp, err := c.do(ctx, request{method: method, path: path, body: body})
			if err != nil {
				return err
			}
			return p.response(resp)
		}, nil
	}
}

func currentPlaybook(ctx context.Context, c *conn, name string) (map[string]any, error) {
	resp, err := c.do(ctx, request{method: http.MethodGet, path: "playbooks/" + name})
	if err != nil {
		return nil, err
	}
	var cur map[string]any
	if err := json.Unmarshal(resp.body, &cur); err != nil {
		return nil, fmt.Errorf("decode playbook: %w", err)
	}
	body := map[string]any{}
	for _, k := range playbookFields {
		body[k] = cur[k]
	}
	return body, nil
}

func overlay(body, changes map[string]any) {
	for k, v := range changes {
		if sub, ok := v.(map[string]any); ok {
			if old, ok := body[k].(map[string]any); ok {
				maps.Copy(old, sub)
				continue
			}
		}
		body[k] = v
	}
}

func parsePlaybookClone(args []string, _ io.Reader) (command, error) {
	set := flags("playbooks clone")
	description := set.String("description", "", "description of the copy")
	if err := parseFlags(set, args, 2, 2); err != nil {
		return nil, err
	}
	source, err := matching(validPlaybook, "playbook name", set.Arg(0))
	if err != nil {
		return nil, err
	}
	target, err := matching(validPlaybook, "playbook name", set.Arg(1))
	if err != nil {
		return nil, err
	}
	return call(request{method: http.MethodPost, path: "playbooks/" + source + "/clone", body: map[string]any{
		"name": target, "description": *description,
	}}), nil
}
