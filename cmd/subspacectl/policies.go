package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var policyCommands = table{
	"list":     plain("policies list", request{method: http.MethodGet, path: "policies"}),
	"show":     byID("policies show", "policies/%s"),
	"versions": byID("policies versions", "policies/%s/versions"),
	"facts":    plain("policies facts", request{method: http.MethodGet, path: "policies/facts"}),
	"create":   jsonBody("policies create", http.MethodPost, "policies", false),
	"update":   jsonBody("policies update", http.MethodPut, "policies/%s", true),
	"delete":   idCall("policies delete", http.MethodDelete, "policies/%s", nil),
	"lint": func(args []string, stdin io.Reader) (command, error) {
		data, err := policyFile("policies lint", args, stdin)
		if err != nil {
			return nil, err
		}
		return confirmed(request{method: http.MethodPost, path: "policies/lint", body: data}, "policy lint", notOK), nil
	},
	"evaluate":  parsePolicyEvaluate,
	"decisions": listing("policies decisions", "policies/decisions", 1000, "outcome", "action", "task-id"),
	"holds":     listing("policies holds", "policy-holds", 1000, "status"),
	"hold":      byID("policies hold", "policy-holds/%s"),
	"approve":   policyDecide("approve"),
	"reject":    policyDecide("reject"),
}

func policyFile(name string, args []string, stdin io.Reader) (json.RawMessage, error) {
	set := flags(name)
	file := set.String("file", "", "policy JSON document or - for stdin")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	if *file == "" {
		return nil, fmt.Errorf("%s requires --file", name)
	}
	data, err := readInput(*file, stdin)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s: input must be a JSON object", name)
	}
	return data, nil
}

func parsePolicyEvaluate(args []string, stdin io.Reader) (command, error) {
	set := flags("policies evaluate")
	action := set.String("action", "", "action to evaluate: assign, claim_task, submit, review, request_spawn, create_task, update_task, approve_plan or merge_ready")
	actor := set.String("actor", "", "acting agent id (default owner)")
	task := set.String("task", "", "task id")
	agent := set.String("agent", "", "target agent id for assign")
	brief := set.String("brief", "", "brief id")
	plan := set.String("plan", "", "plan id")
	at := set.String("at", "", "evaluation time, RFC 3339 (default now)")
	file := set.String("policy", "", "draft policy JSON document or - for stdin (default: the stored policies)")
	var facts listFlag
	set.Var(&facts, "fact", "fact override name=JSON value, repeatable")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	if *action == "" {
		return nil, errors.New("policies evaluate requires --action")
	}
	body := map[string]any{"action": *action}
	for k, v := range map[string]string{"actor_id": *actor, "task_id": *task, "agent_id": *agent, "brief_id": *brief, "plan_id": *plan, "at": *at} {
		if v != "" {
			body[k] = v
		}
	}
	if len(facts) > 0 {
		over := map[string]json.RawMessage{}
		for _, f := range facts {
			name, raw, ok := strings.Cut(f, "=")
			if !ok || name == "" || !json.Valid([]byte(raw)) {
				return nil, fmt.Errorf("--fact %q must be name=JSON", f)
			}
			over[name] = json.RawMessage(raw)
		}
		body["facts"] = over
	}
	if *file != "" {
		data, err := readInput(*file, stdin)
		if err != nil {
			return nil, fmt.Errorf("read policy: %w", err)
		}
		if !json.Valid(data) {
			return nil, errors.New("--policy must be a JSON document")
		}
		body["policy"] = json.RawMessage(data)
	}
	return call(request{method: http.MethodPost, path: "policies/evaluate", body: body}), nil
}

func policyDecide(verb string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		set := flags("policies " + verb)
		note := set.String("note", "", "note for the audit log and the agent")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "policy-holds/" + id + "/" + verb, body: map[string]string{"note": *note}}), nil
	}
}
