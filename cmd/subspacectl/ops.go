package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func plain(name string, req request) parser {
	return func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags(name), args, 0, 0); err != nil {
			return nil, err
		}
		return call(req), nil
	}
}

func idCall(name, method, path string, body any) parser {
	return func(args []string, _ io.Reader) (command, error) {
		id, err := parseID(name, args)
		if err != nil {
			return nil, err
		}
		return call(request{method: method, path: fmt.Sprintf(path, id), body: body}), nil
	}
}

func jsonBody(name, method, path string, withID bool) parser {
	return func(args []string, stdin io.Reader) (command, error) {
		set := flags(name)
		file := set.String("file", "", "JSON object or - for stdin")
		positions := 0
		if withID {
			positions = 1
		}
		if err := parseFlags(set, args, positions, positions); err != nil {
			return nil, err
		}
		if *file == "" {
			return nil, fmt.Errorf("%s requires --file", name)
		}
		target := path
		if withID {
			id, err := identifier(set.Arg(0))
			if err != nil {
				return nil, err
			}
			target = fmt.Sprintf(path, id)
		}
		data, err := readInput(*file, stdin)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return nil, fmt.Errorf("%s: input must be a JSON object", name)
		}
		return call(request{method: method, path: target, body: json.RawMessage(data)}), nil
	}
}

func listing(name, path string, upper int, filters ...string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		set := flags(name)
		values := make([]*string, len(filters))
		for i, f := range filters {
			values[i] = set.String(f, "", "filter by "+f)
		}
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit, upper)
		if err != nil {
			return nil, err
		}
		for i, f := range filters {
			setIf(q, strings.ReplaceAll(f, "-", "_"), *values[i])
		}
		return get(path, q), nil
	}
}

func confirmed(req request, what string, bad func(body []byte) (int, error)) command {
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, req)
		if err != nil {
			return err
		}
		if err := p.response(resp); err != nil {
			return err
		}
		n, err := bad(resp.body)
		if err != nil {
			return fmt.Errorf("invalid %s response: %w", what, err)
		}
		if n > 0 {
			return failf(exitFailure, "%s: %d failed", what, n)
		}
		return nil
	}
}

func countFailures(body []byte) (int, error) {
	var out struct {
		Failed   int               `json:"failed"`
		Failures []json.RawMessage `json:"failures"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	return out.Failed + len(out.Failures), nil
}

func notOK(body []byte) (int, error) {
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	if out.OK {
		return 0, nil
	}
	return 1, nil
}

var scheduleCommands = table{
	"list":    plain("schedules list", request{method: http.MethodGet, path: "schedules"}),
	"show":    byID("schedules show", "schedules/%s"),
	"create":  jsonBody("schedules create", http.MethodPost, "schedules", false),
	"edit":    jsonBody("schedules edit", http.MethodPatch, "schedules/%s", true),
	"enable":  idCall("schedules enable", http.MethodPatch, "schedules/%s", map[string]bool{"enabled": true}),
	"disable": idCall("schedules disable", http.MethodPatch, "schedules/%s", map[string]bool{"enabled": false}),
	"delete":  idCall("schedules delete", http.MethodDelete, "schedules/%s", nil),
	"runs": func(args []string, _ io.Reader) (command, error) {
		set := flags("schedules runs")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		q, err := limited(*limit, 1000)
		if err != nil {
			return nil, err
		}
		return get("schedules/"+id+"/runs", q), nil
	},
	"preview": func(args []string, _ io.Reader) (command, error) {
		set := flags("schedules preview")
		cron := set.String("cron", "", "cron expression")
		zone := set.String("timezone", "", "IANA time zone (default UTC)")
		count := set.Int("count", 0, "number of run times (default 5, at most 20)")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if strings.TrimSpace(*cron) == "" {
			return nil, errors.New("schedules preview requires --cron")
		}
		if *count < 0 || *count > 20 {
			return nil, errors.New("--count must be in 1..20")
		}
		q := setIf(url.Values{}, "cron", *cron, "timezone", *zone)
		if *count > 0 {
			q.Set("count", strconv.Itoa(*count))
		}
		return get("schedules/preview", q), nil
	},
}

var incidentCommands = table{
	"list": listing("incident list", "incident", 1000),
	"start": func(args []string, _ io.Reader) (command, error) {
		set := flags("incident start")
		reason := set.String("reason", "", "why the incident is declared")
		actions := set.String("actions", "", "comma-separated: pause_all, revoke_agents, rotate_leader_tokens, snapshot, notify")
		role := set.String("revoke-role", "", "revoke only agents of this role")
		provider := set.String("revoke-provider", "", "revoke only agents of this provider")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		list := splitList(*actions)
		if strings.TrimSpace(*reason) == "" || len(list) == 0 {
			return nil, errors.New("incident start requires --reason and --actions")
		}
		body := map[string]any{"reason": *reason, "actions": list}
		if *role != "" || *provider != "" {
			body["revoke"] = map[string]string{"role": *role, "provider": *provider}
		}
		return confirmed(request{method: http.MethodPost, path: "incident", body: body}, "incident actions", countFailures), nil
	},
	"end": func(args []string, _ io.Reader) (command, error) {
		set := flags("incident end")
		resume := set.Bool("resume-agents", false, "also resume paused agents")
		note := set.String("note", "", "note")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		body := map[string]any{"resume_agents": *resume, "note": *note}
		return confirmed(request{method: http.MethodPost, path: "incident/" + id + "/end", body: body}, "agent resumes", countFailures), nil
	},
}

var drillCommands = table{
	"list": listing("drills list", "drills", 1000),
	"run": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("drills run"), args, 0, 0); err != nil {
			return nil, err
		}
		return confirmed(request{method: http.MethodPost, path: "drills", body: struct{}{}}, "restore drill", drillFailed), nil
	},
}

func drillFailed(body []byte) (int, error) {
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	if out.Status == "failed" {
		return 1, nil
	}
	return 0, nil
}

func parseCosts(args []string, _ io.Reader) (command, error) {
	set := flags("costs")
	group := set.String("group-by", "", "brief, task, component, agent, kind, model, provider, role or day")
	split := set.String("split-by", "", "second dimension")
	since := set.String("since", "", "RFC 3339 start")
	until := set.String("until", "", "RFC 3339 end")
	brief := set.String("brief", "", "only this brief")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	return get("costs", setIf(url.Values{}, "group_by", *group, "split_by", *split, "since", *since, "until", *until, "brief_id", *brief)), nil
}

var estimateCommands = table{
	"brief": byID("estimates brief", "briefs/%s/estimates"),
	"plan":  byID("estimates plan", "plans/%s/estimates"),
}

var knowledgeCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("knowledge list")
		brief := set.String("brief", "", "filter by brief")
		kind := set.String("kind", "", "decision, convention, pitfall, howto or glossary")
		status := set.String("status", "", "pending, active or superseded")
		query := set.String("q", "", "search text")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit, 100)
		if err != nil {
			return nil, err
		}
		return get("knowledge", setIf(q, "brief_id", *brief, "kind", *kind, "status", *status, "q", *query)), nil
	},
	"show": byID("knowledge show", "knowledge/%s"),
	"add": func(args []string, stdin io.Reader) (command, error) {
		set := flags("knowledge add")
		brief := set.String("brief", "", "brief")
		kind := set.String("kind", "", "decision, convention, pitfall, howto or glossary")
		title := set.String("title", "", "title")
		body := set.String("body", "", "markdown body")
		file := set.String("body-file", "", "read the body from file or stdin")
		tags := set.String("tags", "", "comma-separated tags")
		supersedes := set.String("supersedes", "", "entry this one replaces")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if *brief == "" || *kind == "" || strings.TrimSpace(*title) == "" {
			return nil, errors.New("knowledge add requires --brief, --kind and --title")
		}
		text, err := textInput("body", *body, *file, stdin)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "knowledge", body: map[string]any{
			"brief_id": *brief, "kind": *kind, "title": *title, "body": text, "tags": splitList(*tags), "supersedes": *supersedes,
		}}), nil
	},
	"edit": func(args []string, stdin io.Reader) (command, error) {
		set := flags("knowledge edit")
		kind := set.String("kind", "", "kind")
		title := set.String("title", "", "title")
		body := set.String("body", "", "markdown body")
		file := set.String("body-file", "", "read the body from file or stdin")
		tags := set.String("tags", "", "comma-separated tags")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		seen := visited(set)
		patch := map[string]any{}
		for name, value := range map[string]*string{"kind": kind, "title": title} {
			if seen[name] {
				patch[name] = *value
			}
		}
		if seen["body"] || seen["body-file"] {
			if patch["body"], err = textInput("body", *body, *file, stdin); err != nil {
				return nil, err
			}
		}
		if seen["tags"] {
			patch["tags"] = splitList(*tags)
		}
		if len(patch) == 0 {
			return nil, errors.New("knowledge edit needs at least one field to change")
		}
		return call(request{method: http.MethodPatch, path: "knowledge/" + id, body: patch}), nil
	},
	"delete":  idCall("knowledge delete", http.MethodDelete, "knowledge/%s", nil),
	"approve": idCall("knowledge approve", http.MethodPost, "knowledge/%s/approve", struct{}{}),
	"pin": func(args []string, _ io.Reader) (command, error) {
		set := flags("knowledge pin")
		off := set.Bool("off", false, "unpin instead")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "knowledge/" + id + "/pin", body: map[string]bool{"pinned": !*off}}), nil
	},
	"supersede": func(args []string, _ io.Reader) (command, error) {
		set := flags("knowledge supersede")
		by := set.String("by", "", "newer entry of the same brief")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "knowledge/" + id + "/supersede", body: map[string]string{"by": *by}}), nil
	},
}

var announcementCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("announcements list")
		status := set.String("status", "", "active (default) or all")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		return get("announcements", setIf(url.Values{}, "status", *status)), nil
	},
	"publish": func(args []string, stdin io.Reader) (command, error) {
		set := flags("announcements publish")
		text := set.String("text", "", "announcement text")
		file := set.String("text-file", "", "read the text from file or stdin")
		expires := set.String("expires-at", "", "RFC 3339 expiry")
		brief := set.String("brief", "", "only agents working on this brief")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		value, err := textInput("text", *text, *file, stdin)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"text": value, "brief_id": *brief, "expires_at": nil}
		if *expires != "" {
			if _, err := time.Parse(time.RFC3339, *expires); err != nil {
				return nil, errors.New("--expires-at must be an RFC 3339 time")
			}
			body["expires_at"] = *expires
		}
		return call(request{method: http.MethodPost, path: "announcements", body: body}), nil
	},
	"delete": idCall("announcements delete", http.MethodDelete, "announcements/%s", nil),
}

func quotaFlags(set *flag.FlagSet) func() (map[string]any, error) {
	cost := set.Float64("max-cost-eur-per-day", -1, "daily cost cap in EUR, 0 = unlimited")
	tasks := set.Int("max-tasks-per-day", -1, "tasks per day, 0 = unlimited")
	concurrent := set.Int("max-concurrent-tasks", -1, "concurrent tasks, 0 = unlimited")
	return func() (map[string]any, error) {
		if *cost < 0 || *tasks < 0 || *concurrent < 0 {
			return nil, errors.New("set --max-cost-eur-per-day, --max-tasks-per-day and --max-concurrent-tasks (0 = unlimited)")
		}
		return map[string]any{"max_cost_eur_per_day": *cost, "max_tasks_per_day": *tasks, "max_concurrent_tasks": *concurrent}, nil
	}
}

var quotaCommands = table{
	"show": byID("quotas show", "agents/%s"),
	"set": func(args []string, _ io.Reader) (command, error) {
		set := flags("quotas set")
		quota := quotaFlags(set)
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		body, err := quota()
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPut, path: "agents/" + id + "/quota", body: body}), nil
	},
	"bulk": func(args []string, stdin io.Reader) (command, error) {
		set := flags("quotas bulk")
		quota := quotaFlags(set)
		if err := parseFlags(set, args, 1, maxBulk); err != nil {
			return nil, err
		}
		ids, err := collectIDs(set.Args(), stdin)
		if err != nil {
			return nil, err
		}
		body, err := quota()
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "agents/quota-bulk", body: map[string]any{"ids": ids, "quota": body}}), nil
	},
}

var retentionCommands = table{
	"status":  plain("retention status", request{method: http.MethodGet, path: "retention"}),
	"run":     plain("retention run", request{method: http.MethodPost, path: "retention/run", body: struct{}{}}),
	"restore": idCall("retention restore", http.MethodPost, "retention/segments/%s/restore", struct{}{}),
	"verify": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("retention verify"), args, 0, 0); err != nil {
			return nil, err
		}
		return confirmed(request{method: http.MethodGet, path: "retention/verify"}, "retention verification", notOK), nil
	},
}

var webhookCommands = table{
	"list": plain("webhooks-out list", request{method: http.MethodGet, path: "webhooks-out"}),
	"show": byID("webhooks-out show", "webhooks-out/%s"),
	"create": func(args []string, _ io.Reader) (command, error) {
		set := flags("webhooks-out create")
		target := set.String("target-url", "", "delivery URL")
		events := set.String("events", "", "comma-separated event types or family wildcards such as decision.*")
		brief := set.String("brief", "", "only events of this brief")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		list := splitList(*events)
		if strings.TrimSpace(*target) == "" || len(list) == 0 {
			return nil, errors.New("webhooks-out create requires --target-url and --events")
		}
		return call(request{method: http.MethodPost, path: "webhooks-out", body: map[string]any{"url": *target, "events": list, "brief_id": *brief}}), nil
	},
	"delete": idCall("webhooks-out delete", http.MethodDelete, "webhooks-out/%s", nil),
	"deliveries": func(args []string, _ io.Reader) (command, error) {
		set := flags("webhooks-out deliveries")
		state := set.String("state", "", "pending, delivered or dead")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		q, err := limited(*limit, 1000)
		if err != nil {
			return nil, err
		}
		return get("webhooks-out/"+id+"/deliveries", setIf(q, "state", *state)), nil
	},
	"redeliver": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("webhooks-out redeliver"), args, 2, 2); err != nil {
			return nil, err
		}
		hook, err := identifier(args[0])
		if err != nil {
			return nil, err
		}
		delivery, err := identifier(args[1])
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "webhooks-out/" + hook + "/deliveries/" + delivery + "/redeliver", body: struct{}{}}), nil
	},
}

var breakerCommands = table{
	"list":  plain("breakers list", request{method: http.MethodGet, path: "breakers"}),
	"reset": idCall("breakers reset", http.MethodPost, "breakers/%s/reset", struct{}{}),
}

var spendLimitCommands = table{
	"get": plain("spend-limits get", request{method: http.MethodGet, path: "limits"}),
	"set": func(args []string, _ io.Reader) (command, error) {
		set := flags("spend-limits set")
		daily := set.Float64("daily-eur", -1, "daily cap in EUR, 0 = off")
		weekly := set.Float64("weekly-eur", -1, "weekly cap in EUR, 0 = off")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if *daily < 0 || *weekly < 0 {
			return nil, errors.New("spend-limits set requires --daily-eur and --weekly-eur (0 = off)")
		}
		return call(request{method: http.MethodPut, path: "limits", body: map[string]float64{"daily_cost_eur": *daily, "weekly_cost_eur": *weekly}}), nil
	},
}

var tokenCommands = table{
	"list":   plain("tokens list", request{method: http.MethodGet, path: "tokens"}),
	"revoke": idCall("tokens revoke", http.MethodPost, "tokens/%s/revoke", struct{}{}),
	"create": func(args []string, _ io.Reader) (command, error) {
		set := flags("tokens create")
		name := set.String("name", "", "token name")
		scope := set.String("scope", "", "viewer or owner")
		expires := set.String("expires-at", "", "RFC 3339 expiry")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if strings.TrimSpace(*name) == "" || (*scope != "viewer" && *scope != "owner") {
			return nil, errors.New("tokens create requires --name and --scope viewer|owner")
		}
		body := map[string]any{"name": *name, "scope": *scope, "expires_at": nil}
		if *expires != "" {
			if _, err := time.Parse(time.RFC3339, *expires); err != nil {
				return nil, errors.New("--expires-at must be an RFC 3339 time")
			}
			body["expires_at"] = *expires
		}
		return call(request{method: http.MethodPost, path: "tokens", body: body}), nil
	},
}

func parseInsights(args []string, _ io.Reader) (command, error) {
	set := flags("insights")
	severity := set.String("severity", "", "warning or high")
	if err := parseFlags(set, args, 0, 1); err != nil {
		return nil, err
	}
	if set.NArg() == 1 {
		if *severity != "" {
			return nil, errors.New("--severity applies to the insights of all agents")
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return get("agents/"+id+"/insights", nil), nil
	}
	return get("insights", setIf(url.Values{}, "severity", *severity)), nil
}

func parsePerformance(args []string, _ io.Reader) (command, error) {
	set := flags("agents performance")
	role := set.String("role", "", "filter by role")
	if err := parseFlags(set, args, 0, 1); err != nil {
		return nil, err
	}
	if set.NArg() == 1 {
		if *role != "" {
			return nil, errors.New("--role applies to the statistics of all agents")
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return get("agents/"+id+"/stats", nil), nil
	}
	return get("agent-stats", setIf(url.Values{}, "role", *role)), nil
}

var replayCommands = table{
	"show": func(args []string, _ io.Reader) (command, error) {
		set := flags("replay show")
		at := set.String("at", "", "audit id or RFC 3339 time (default latest)")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return get("briefs/"+id+"/replay", setIf(url.Values{}, "at", *at)), nil
	},
	"timeline": byID("replay timeline", "briefs/%s/replay/timeline"),
	"diff": func(args []string, _ io.Reader) (command, error) {
		set := flags("replay diff")
		from := set.String("from", "", "audit id or RFC 3339 time (default the beginning)")
		to := set.String("to", "", "audit id or RFC 3339 time (default latest)")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return get("briefs/"+id+"/replay/diff", setIf(url.Values{}, "from", *from, "to", *to)), nil
	},
}

func parseOpenAPI(args []string, _ io.Reader) (command, error) {
	set := flags("openapi")
	out := set.String("out", "", "write the document to a file")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "openapi.json", maxResponse: transferMax})
		if err != nil {
			return err
		}
		if *out != "" {
			return save(*out, resp.body, p, nil)
		}
		return p.response(resp)
	}, nil
}

var charterCommands = table{
	"get": plain("charter get", request{method: http.MethodGet, path: "charter"}),
	"set": func(args []string, stdin io.Reader) (command, error) {
		set := flags("charter set")
		text := set.String("text", "", "charter text")
		file := set.String("text-file", "", "read the charter from file or stdin")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		value, err := textInput("text", *text, *file, stdin)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPut, path: "charter", body: map[string]string{"text": value}}), nil
	},
}

var notifyCommands = table{
	"status": plain("notify status", request{method: http.MethodGet, path: "notify/status"}),
	"test":   plain("notify test", request{method: http.MethodPost, path: "notify/test", body: struct{}{}}),
}

func parseDigest(args []string, _ io.Reader) (command, error) {
	set := flags("digest")
	window := set.String("window", "", "window to preview, such as 24h")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	return get("digest/preview", setIf(url.Values{}, "window", *window)), nil
}

func parseLogs(args []string, _ io.Reader) (command, error) {
	set := flags("logs")
	level := set.String("level", "", "minimum level")
	since := set.Int64("since", 0, "only lines after this ID")
	limit := set.Int("limit", 0, "maximum results")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	q, err := limited(*limit, 1000)
	if err != nil {
		return nil, err
	}
	if *since < 0 {
		return nil, errors.New("--since must not be negative")
	}
	if *since > 0 {
		q.Set("since", strconv.FormatInt(*since, 10))
	}
	return get("logs", setIf(q, "level", *level)), nil
}

func parseRotateToken(args []string, _ io.Reader) (command, error) {
	set := flags("agents rotate-token")
	expires := set.String("expires-in", "", "lifetime of the new token such as 720h")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	return call(request{method: http.MethodPost, path: "agents/" + id + "/rotate-token", body: map[string]string{"expires_in": *expires}}), nil
}
