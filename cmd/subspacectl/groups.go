package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type table map[string]parser

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

var (
	validDecision  = regexp.MustCompile(`^(q|plan|merge|unblock|stale):[a-zA-Z0-9_-]+$`)
	validRecipient = regexp.MustCompile(`^(leader|(agent|brief|component):[a-zA-Z0-9_-]+)$`)
)

const maxBulk = 1000

func names(t table) []string { return slices.Sorted(maps.Keys(t)) }

func grouped(group string, t table) parser {
	return func(args []string, stdin io.Reader) (command, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("%s requires one of: %s", group, strings.Join(names(t), ", "))
		}
		if args[0] == "--help" || args[0] == "-h" {
			return nil, flag.ErrHelp
		}
		p, ok := t[args[0]]
		if !ok {
			return nil, fmt.Errorf("unknown %s command %q (see --help)", group, args[0])
		}
		return p(args[1:], stdin)
	}
}

func get(path string, query url.Values) command {
	return call(request{method: http.MethodGet, path: path, query: query})
}

func matching(re *regexp.Regexp, what, value string) (string, error) {
	if !re.MatchString(value) {
		return "", fmt.Errorf("invalid %s %q", what, value)
	}
	return value, nil
}

const maxLimit = 1000

func limited(limit int) (url.Values, error) {
	q := url.Values{}
	if limit < 0 || limit > maxLimit {
		return nil, fmt.Errorf("--limit must be in 1..%d", maxLimit)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q, nil
}

func setIf(q url.Values, kv ...string) url.Values {
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	return q
}

func visited(set *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	set.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	return seen
}

func textInput(label, value, file string, stdin io.Reader) (string, error) {
	if (value == "") == (file == "") {
		return "", fmt.Errorf("provide exactly one of --%s or --%s-file", label, label)
	}
	if file != "" {
		data, err := readInput(file, stdin)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", label, err)
		}
		value = string(data)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	return value, nil
}

func parseVars(list []string) (url.Values, error) {
	q := url.Values{}
	for _, entry := range list {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--var %q must be name=value", entry)
		}
		q.Set(key, value)
	}
	return q, nil
}

func collectIDs(args []string, stdin io.Reader) ([]string, error) {
	if len(args) == 1 && args[0] == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read identifiers: %w", err)
		}
		args = strings.Fields(string(data))
	}
	seen := map[string]bool{}
	var ids []string
	for _, arg := range args {
		id, err := identifier(arg)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no identifiers given")
	}
	if len(ids) > maxBulk {
		return nil, fmt.Errorf("at most %d identifiers per call", maxBulk)
	}
	return ids, nil
}

func bulkArgs(name string, set *flag.FlagSet, args []string, stdin io.Reader, actions ...string) (string, []string, error) {
	if err := parseFlags(set, args, 2, maxBulk+1); err != nil {
		return "", nil, err
	}
	act := set.Arg(0)
	if !slices.Contains(actions, act) {
		return "", nil, fmt.Errorf("%s: action must be one of %s", name, strings.Join(actions, ", "))
	}
	ids, err := collectIDs(set.Args()[1:], stdin)
	return act, ids, err
}

var decisionCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("decisions list")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit)
		if err != nil {
			return nil, err
		}
		return get("decisions", q), nil
	},
	"next": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("decisions next"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("decisions/next", nil), nil
	},
	"act": func(args []string, _ io.Reader) (command, error) {
		set := flags("decisions act")
		act := set.String("action", "", "decision action")
		answer := set.String("answer", "", "answer text")
		note := set.String("note", "", "note")
		reason := set.String("reason", "", "reason")
		status := set.String("status", "", "status for unblock")
		snooze := set.String("snooze", "", "snooze duration for skip")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := matching(validDecision, "decision id", set.Arg(0))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(*act) == "" {
			return nil, fmt.Errorf("decisions act requires --action")
		}
		body := map[string]string{"action": *act}
		for key, value := range map[string]string{"answer": *answer, "note": *note, "reason": *reason, "status": *status, "snooze": *snooze} {
			if value != "" {
				body[key] = value
			}
		}
		return call(request{method: http.MethodPost, path: "decisions/" + id, body: body}), nil
	},
}

var chatCommands = table{
	"send": func(args []string, stdin io.Reader) (command, error) {
		set := flags("chat send")
		to := set.String("to", "", "agent:ID, brief:ID or leader")
		body := set.String("body", "", "message text")
		file := set.String("body-file", "", "read message from file or stdin")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if _, err := matching(validRecipient, "recipient", *to); err != nil {
			return nil, err
		}
		text, err := textInput("body", *body, *file, stdin)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "messages", body: map[string]string{"to": *to, "body": text}}), nil
	},
	"list": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("chat list"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("conversations", nil), nil
	},
	"show": byID("chat show", "conversations/%s"),
}

func parseSearch(args []string, _ io.Reader) (command, error) {
	set := flags("search")
	kind := set.String("kind", "", "task, message, agent or brief")
	limit := set.Int("limit", 0, "maximum results")
	if err := parseFlags(set, args, 1, 64); err != nil {
		return nil, err
	}
	if *limit < 0 || *limit > 100 {
		return nil, fmt.Errorf("--limit must be in 1..100")
	}
	q := setIf(url.Values{"q": {strings.Join(set.Args(), " ")}}, "kind", *kind)
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	return get("search/", q), nil
}

func parseReport(args []string, _ io.Reader) (command, error) {
	set := flags("report")
	format := set.String("format", "json", "json or md")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	switch *format {
	case "json":
		return get("briefs/"+id+"/report", nil), nil
	case "md":
		return call(request{method: http.MethodGet, path: "briefs/" + id + "/report.md", accept: "text/markdown"}), nil
	}
	return nil, fmt.Errorf("--format must be json or md")
}

var planCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("plans list")
		brief := set.String("brief", "", "filter by brief")
		status := set.String("status", "", "filter by status")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		return get("plans", setIf(url.Values{}, "brief_id", *brief, "status", *status)), nil
	},
	"show":    byID("plans show", "plans/%s"),
	"approve": action("approve-plan"),
	"reject":  action("reject-plan"),
	"add-task": func(args []string, stdin io.Reader) (command, error) {
		set := flags("plans add-task")
		title := set.String("title", "", "task title")
		spec := set.String("spec", "", "task spec")
		specFile := set.String("spec-file", "", "read the spec from file or stdin")
		repo := set.String("repo", "", "repository")
		role := set.String("role", "", "worker or tester")
		deps := set.String("depends-on", "", "comma-separated task IDs")
		minutes := set.Int("max-minutes", 0, "task minute budget")
		cost := set.Float64("max-cost-eur", 0, "task cost budget")
		var criteria listFlag
		set.Var(&criteria, "criterion", "acceptance criterion (repeatable)")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		plan, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(*title) == "" || strings.TrimSpace(*repo) == "" {
			return nil, fmt.Errorf("plans add-task requires --title and --repo")
		}
		text, err := textInput("spec", *spec, *specFile, stdin)
		if err != nil {
			return nil, err
		}
		body := map[string]any{
			"title": *title, "spec": text, "repo": *repo,
			"acceptance_criteria": append([]string{}, criteria...), "depends_on": splitList(*deps),
		}
		if *role != "" {
			body["role"] = *role
		}
		if *minutes < 0 || *cost < 0 {
			return nil, fmt.Errorf("budgets must not be negative")
		}
		if *minutes > 0 || *cost > 0 {
			body["budget"] = map[string]any{"max_minutes": *minutes, "max_cost_eur": *cost}
		}
		return call(request{method: http.MethodPost, path: "plans/" + plan + "/tasks", body: body}), nil
	},
	"drop-task": func(args []string, _ io.Reader) (command, error) {
		set := flags("plans drop-task")
		if err := parseFlags(set, args, 2, 2); err != nil {
			return nil, err
		}
		plan, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		task, err := identifier(set.Arg(1))
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodDelete, path: "plans/" + plan + "/tasks/" + task}), nil
	},
	"edit-task": parseEditTask,
}

func parseEditTask(args []string, stdin io.Reader) (command, error) {
	set := flags("plans edit-task")
	title := set.String("title", "", "task title")
	spec := set.String("spec", "", "task spec")
	specFile := set.String("spec-file", "", "read the spec from file or stdin")
	repo := set.String("repo", "", "repository")
	role := set.String("role", "", "worker or tester")
	deps := set.String("depends-on", "", "comma-separated task IDs")
	minutes := set.Int("max-minutes", 0, "task minute budget")
	cost := set.Float64("max-cost-eur", 0, "task cost budget")
	var criteria listFlag
	set.Var(&criteria, "criterion", "acceptance criterion (repeatable)")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	seen := visited(set)
	body := map[string]any{}
	for name, value := range map[string]*string{"title": title, "repo": repo, "role": role} {
		if seen[name] {
			body[name] = *value
		}
	}
	if seen["spec"] || seen["spec-file"] {
		if body["spec"], err = textInput("spec", *spec, *specFile, stdin); err != nil {
			return nil, err
		}
	}
	if seen["criterion"] {
		body["acceptance_criteria"] = append([]string{}, criteria...)
	}
	if seen["depends-on"] {
		body["depends_on"] = splitList(*deps)
	}
	if seen["max-minutes"] != seen["max-cost-eur"] {
		return nil, fmt.Errorf("--max-minutes and --max-cost-eur must be set together")
	}
	if seen["max-minutes"] {
		if *minutes < 0 || *cost < 0 {
			return nil, fmt.Errorf("budgets must not be negative")
		}
		body["budget"] = map[string]any{"max_minutes": *minutes, "max_cost_eur": *cost}
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("plans edit-task needs at least one field to change")
	}
	return call(request{method: http.MethodPatch, path: "tasks/" + id, body: body}), nil
}

var taskCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("tasks list")
		brief := set.String("brief", "", "filter by brief")
		status := set.String("status", "", "filter by status")
		assignee := set.String("assignee", "", "filter by assignee")
		role := set.String("role", "", "filter by role")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit)
		if err != nil {
			return nil, err
		}
		return get("tasks", setIf(q, "brief_id", *brief, "status", *status, "assignee", *assignee, "role", *role)), nil
	},
	"show":  byID("tasks show", "tasks/%s"),
	"pr":    byID("tasks pr", "tasks/%s/pr"),
	"scope": byID("tasks scope", "tasks/%s/scope"),
	"gates": byID("tasks gates", "tasks/%s/gates"),
	"bulk":  parseTaskBulk,
}

func parseTaskBulk(args []string, stdin io.Reader) (command, error) {
	set := flags("tasks bulk")
	note := set.String("note", "", "note for done and reopen")
	reason := set.String("reason", "", "reason for cancel")
	status := set.String("status", "", "status for reopen")
	act, ids, err := bulkArgs("tasks bulk", set, args, stdin, "done", "cancel", "reopen")
	if err != nil {
		return nil, err
	}
	build := func(id string) request {
		base := "tasks/" + id + "/"
		switch act {
		case "done":
			return request{method: http.MethodPost, path: base + "done", body: map[string]string{"note": *note}}
		case "cancel":
			return request{method: http.MethodPost, path: base + "cancel", body: map[string]string{"reason": *reason}}
		}
		return request{method: http.MethodPost, path: base + "reopen", body: map[string]string{"status": *status, "note": *note}}
	}
	return bulk(act, ids, build), nil
}
