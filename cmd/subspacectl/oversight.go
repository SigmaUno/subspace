package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
)

var threadCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("threads list")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 2, 2); err != nil {
			return nil, err
		}
		path, err := threadPath(set.Arg(0), set.Arg(1))
		if err != nil {
			return nil, err
		}
		q, err := limited(*limit, 1000)
		if err != nil {
			return nil, err
		}
		return get(path, q), nil
	},
	"post": func(args []string, stdin io.Reader) (command, error) {
		set := flags("threads post")
		body := set.String("body", "", "message text")
		file := set.String("body-file", "", "read the message from a file or stdin")
		if err := parseFlags(set, args, 2, 2); err != nil {
			return nil, err
		}
		path, err := threadPath(set.Arg(0), set.Arg(1))
		if err != nil {
			return nil, err
		}
		text, err := textInput("body", *body, *file, stdin)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: path, body: map[string]string{"body": text}}), nil
	},
}

var validThreadKind = regexp.MustCompile(`^(agent|task|brief)$`)

func threadPath(kind, id string) (string, error) {
	if _, err := matching(validThreadKind, "thread kind", kind); err != nil {
		return "", errors.New("thread kind must be agent, task or brief")
	}
	id, err := identifier(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("threads/%s/%s/messages", kind, id), nil
}

var logLevelCommands = table{
	"get": plain("log-level get", request{method: http.MethodGet, path: "log-level"}),
	"set": func(args []string, _ io.Reader) (command, error) {
		set := flags("log-level set")
		level := set.String("level", "", "debug, info, warn or error")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if *level == "" {
			return nil, errors.New("log-level set requires --level")
		}
		return call(request{method: http.MethodPut, path: "log-level", body: map[string]string{"level": *level}}), nil
	},
}

var tokenLeakCommands = table{
	"reissue": idCall("token-leaks reissue", http.MethodPost, "token-leaks/%s/reissue", struct{}{}),
}

var actionLinkCommands = table{
	"create": func(args []string, _ io.Reader) (command, error) {
		set := flags("action-links create")
		decision := set.String("decision", "", "decision ID such as plan:P-1 or q:Q-1")
		action := set.String("action", "", "action the link performs")
		option := set.Int("option", 0, "option index for answers")
		ttl := set.String("ttl", "", "link lifetime such as 24h")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if _, err := matching(validDecision, "decision", *decision); err != nil {
			return nil, err
		}
		if *action == "" {
			return nil, errors.New("action-links create requires --action")
		}
		body := map[string]any{"decision_id": *decision, "action": *action, "option": *option}
		if *ttl != "" {
			body["ttl"] = *ttl
		}
		return call(request{method: http.MethodPost, path: "action-links", body: body}), nil
	},
}

var hillclimbCommands = table{
	"list":   listing("hillclimb list", "hillclimb/jobs", 1000),
	"create": jsonBody("hillclimb create", http.MethodPost, "hillclimb/jobs", false),
	"series": byID("hillclimb series", "hillclimb/jobs/%s/series"),
}

func parseTimeseries(args []string, _ io.Reader) (command, error) {
	set := flags("timeseries")
	metric := set.String("metric", "", "tasks_done, tasks_created, cost_eur, active_agents, tokens or review_rounds")
	bucket := set.String("bucket", "", "5m, 1h or 1d")
	since := set.String("since", "", "RFC 3339 start time")
	brief := set.String("brief-id", "", "limit to one brief")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	if *metric == "" || *bucket == "" {
		return nil, errors.New("timeseries requires --metric and --bucket")
	}
	return get("timeseries", setIf(url.Values{"metric": {*metric}, "bucket": {*bucket}}, "since", *since, "brief_id", *brief)), nil
}
