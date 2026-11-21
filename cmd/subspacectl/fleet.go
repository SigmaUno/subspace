package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func resultJSON(body []byte) json.RawMessage {
	if len(bytes.TrimSpace(body)) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(body)
}

func errorBody(err error) map[string]any {
	var ce *cliError
	if errors.As(err, &ce) {
		out := map[string]any{"message": ce.msg}
		if ce.code != "" {
			out["code"] = ce.code
		}
		if ce.status != 0 {
			out["status"] = ce.status
		}
		return out
	}
	return map[string]any{"message": err.Error()}
}

func bulk(act string, ids []string, build func(id string) request) command {
	return func(ctx context.Context, c *conn, p *printer) error {
		results := make([]map[string]any, 0, len(ids))
		failed := 0
		for _, id := range ids {
			resp, err := c.do(ctx, build(id))
			if err != nil && ctx.Err() != nil {
				return err
			}
			if err != nil {
				failed++
				results = append(results, map[string]any{"id": id, "ok": false, "error": errorBody(err)})
				continue
			}
			results = append(results, map[string]any{"id": id, "ok": true, "result": resultJSON(resp.body)})
		}
		summary := map[string]any{"action": act, "total": len(ids), "failed": failed, "results": results}
		if err := p.value(summary); err != nil {
			return err
		}
		if failed > 0 {
			return failf(exitFailure, "%d of %d %s operations failed", failed, len(ids), act)
		}
		return nil
	}
}

var agentCommands = table{
	"register": func(args []string, _ io.Reader) (command, error) {
		return single(parseRegistration("agents register", args))
	},
	"stats":       parseAgentStats,
	"bulk-action": parseAgentBulk,
}

func parseAgents(args []string, stdin io.Reader) (command, error) {
	if len(args) > 0 {
		if p, ok := agentCommands[args[0]]; ok {
			return p(args[1:], stdin)
		}
	}
	return single(parseList("agents", args))
}

func parseAgentStats(args []string, _ io.Reader) (command, error) {
	if err := parseFlags(flags("agents stats"), args, 0, 0); err != nil {
		return nil, err
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "agents"})
		if err != nil {
			return err
		}
		var list struct {
			Agents []struct {
				Role          string  `json:"role"`
				Kind          string  `json:"kind"`
				Provider      string  `json:"provider"`
				Status        string  `json:"status"`
				CurrentTaskID *string `json:"current_task_id"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(resp.body, &list); err != nil {
			return fmt.Errorf("invalid agents response: %w", err)
		}
		byRole, byStatus, byProvider, byKind := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
		working := 0
		for _, a := range list.Agents {
			byRole[a.Role]++
			byStatus[a.Status]++
			byProvider[a.Provider]++
			byKind[a.Kind]++
			if a.CurrentTaskID != nil && *a.CurrentTaskID != "" {
				working++
			}
		}
		return p.value(map[string]any{
			"total": len(list.Agents), "working": working,
			"by_role": byRole, "by_status": byStatus, "by_provider": byProvider, "by_kind": byKind,
		})
	}, nil
}

func parseAgentBulk(args []string, stdin io.Reader) (command, error) {
	set := flags("agents bulk-action")
	reason := set.String("reason", "", "reason for pause, revoke and kill")
	act, ids, err := bulkArgs("agents bulk-action", set, args, stdin, "pause", "resume", "revoke", "kill")
	if err != nil {
		return nil, err
	}
	build := func(id string) request {
		switch act {
		case "pause":
			return request{method: http.MethodPost, path: "bots/" + id + "/pause", body: map[string]string{"reason": *reason}}
		case "resume":
			return request{method: http.MethodPost, path: "agents/" + id + "/resume", body: map[string]string{}}
		case "revoke":
			return request{method: http.MethodPost, path: "agents/" + id + "/revoke", body: map[string]string{"reason": *reason}}
		}
		return request{method: http.MethodPost, path: "bots/" + id + "/kill", body: map[string]string{"reason": *reason}}
	}
	return bulk(act, ids, build), nil
}

var spawnCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("spawns list")
		brief := set.String("brief", "", "filter by brief")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		return get("spawns", setIf(url.Values{}, "brief_id", *brief)), nil
	},
	"stop": func(args []string, _ io.Reader) (command, error) {
		set := flags("spawns stop")
		reason := set.String("reason", "", "reason")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		return stopSpawn(id, *reason), nil
	},
}

func stopSpawn(id, reason string) command {
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "spawns"})
		if err != nil {
			return err
		}
		var list struct {
			Spawns []struct {
				ID      string `json:"id"`
				AgentID string `json:"agent_id"`
			} `json:"spawns"`
		}
		if err := json.Unmarshal(resp.body, &list); err != nil {
			return fmt.Errorf("invalid spawns response: %w", err)
		}
		for _, s := range list.Spawns {
			if s.ID != id {
				continue
			}
			killed, err := c.do(ctx, request{method: http.MethodPost, path: "bots/" + s.AgentID + "/kill", body: map[string]string{"reason": reason}})
			if err != nil {
				return err
			}
			return p.value(map[string]any{"spawn": id, "agent_id": s.AgentID, "result": resultJSON(killed.body)})
		}
		return &cliError{exit: exitNotFound, status: http.StatusNotFound, code: "not_found", msg: fmt.Sprintf("spawn %s not found", id)}
	}
}

func autopilotSwitch(name string, enabled bool) parser {
	return func(args []string, _ io.Reader) (command, error) {
		id, err := parseID("autopilot "+name, args)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "autopilot/briefs/" + id, body: map[string]bool{"enabled": enabled}}), nil
	}
}

var autopilotCommands = table{
	"status": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("autopilot status"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("autopilot", nil), nil
	},
	"enable":  autopilotSwitch("enable", true),
	"disable": autopilotSwitch("disable", false),
}

var mergequeueCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("mergequeue list")
		repo := set.String("repo", "", "filter by repository")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		return get("mergequeue/candidates", setIf(url.Values{}, "repo", *repo)), nil
	},
	"show": byID("mergequeue show", "mergequeue/candidates/%s"),
	"enqueue": func(args []string, _ io.Reader) (command, error) {
		set := flags("mergequeue enqueue")
		pr := set.Int("pr", 0, "pull request number (default from the task)")
		base := set.String("base", "", "base branch (default from the server config)")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		if *pr < 0 {
			return nil, fmt.Errorf("--pr must not be negative")
		}
		body := map[string]any{"task_id": id}
		if *pr > 0 {
			body["pr"] = *pr
		}
		if *base != "" {
			body["base"] = *base
		}
		return call(request{method: http.MethodPost, path: "mergequeue/candidates", body: body}), nil
	},
}

var backupCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("backups list"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("backups", nil), nil
	},
	"create": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("backups create"), args, 0, 0); err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPost, path: "backup", body: map[string]string{}}), nil
	},
}

var auditCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("audit list")
		agent := set.String("agent", "", "filter by agent")
		task := set.String("task", "", "filter by task")
		tool := set.String("tool", "", "filter by tool")
		since := set.Int64("since", 0, "only events after this ID")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit, 1000)
		if err != nil {
			return nil, err
		}
		if *since < 0 {
			return nil, fmt.Errorf("--since must not be negative")
		}
		setIf(q, "agent_id", *agent, "task_id", *task, "tool", *tool)
		if *since > 0 {
			q.Set("since", strconv.FormatInt(*since, 10))
		}
		return get("audit", q), nil
	},
	"verify": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("audit verify"), args, 0, 0); err != nil {
			return nil, err
		}
		return verifyAudit, nil
	},
	"export": func(args []string, _ io.Reader) (command, error) {
		set := flags("audit export")
		since := set.Int64("since", 0, "only events after this ID")
		out := set.String("out", "", "write to a file instead of stdout")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if *since < 0 {
			return nil, fmt.Errorf("--since must not be negative")
		}
		req := request{
			method: http.MethodGet, path: "audit/export", accept: "application/x-ndjson",
			maxResponse: transferMax, timeout: transferTimeout, query: url.Values{},
		}
		if *since > 0 {
			req.query.Set("since", strconv.FormatInt(*since, 10))
		}
		return func(ctx context.Context, c *conn, p *printer) error {
			resp, err := c.do(ctx, req)
			if err != nil {
				return err
			}
			if *out != "" {
				return save(*out, resp.body, p, nil)
			}
			return p.response(resp)
		}, nil
	},
}

func verifyAudit(ctx context.Context, c *conn, p *printer) error {
	resp, err := c.do(ctx, request{method: http.MethodGet, path: "audit/verify"})
	if err != nil {
		return err
	}
	if err := p.response(resp); err != nil {
		return err
	}
	var result struct {
		OK       bool  `json:"ok"`
		BrokenID int64 `json:"broken_id"`
	}
	if err := json.Unmarshal(resp.body, &result); err != nil {
		return fmt.Errorf("invalid audit verify response: %w", err)
	}
	if !result.OK {
		return failf(exitFailure, "audit chain is broken at event %d", result.BrokenID)
	}
	return nil
}

func parseLimits(args []string, _ io.Reader) (command, error) {
	set := flags("limits")
	provider := set.String("provider", "", "filter by provider")
	model := set.String("model", "", "filter by model")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "capacity"})
		if err != nil {
			return err
		}
		var view map[string]json.RawMessage
		if err := json.Unmarshal(resp.body, &view); err != nil {
			return fmt.Errorf("invalid capacity response: %w", err)
		}
		var all []map[string]any
		if raw := view["limits"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &all); err != nil {
				return fmt.Errorf("invalid capacity limits: %w", err)
			}
		}
		kept := make([]map[string]any, 0, len(all))
		for _, l := range all {
			if (*provider == "" || l["provider"] == *provider) && (*model == "" || l["model"] == *model) {
				kept = append(kept, l)
			}
		}
		out := make(map[string]any, len(view))
		for k, v := range view {
			out[k] = v
		}
		out["limits"] = kept
		return p.value(out)
	}, nil
}
