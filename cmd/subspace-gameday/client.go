package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	Base  string
	Token string
	HC    *http.Client
}

func NewClient(base, token string, timeout time.Duration) *Client {
	tr := &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 5 * time.Second, DisableKeepAlives: false}
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HC: &http.Client{Timeout: timeout, Transport: tr}}
}

func (c *Client) With(base string, timeout time.Duration) *Client {
	return NewClient(base, c.Token, timeout)
}

func (c *Client) Close() { c.HC.CloseIdleConnections() }

func (c *Client) REST(ctx context.Context, method, path string, body any) (int, map[string]any, error) {
	return c.do(ctx, c.Token, method, "/api/v1"+path, body)
}

func (c *Client) do(ctx context.Context, token, method, path string, body any) (int, map[string]any, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

func (c *Client) Must(ctx context.Context, method, path string, body any, want int) (map[string]any, error) {
	code, out, err := c.REST(ctx, method, path, body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if code != want {
		return nil, fmt.Errorf("%s %s: status %d, want %d: %v", method, path, code, want, out["error"])
	}
	return out, nil
}

var rpcID atomic.Int64

func (c *Client) Tool(ctx context.Context, token, name string, args any) (map[string]any, string, error) {
	req := map[string]any{"jsonrpc": "2.0", "id": rpcID.Add(1), "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	code, out, err := c.do(ctx, token, http.MethodPost, "/mcp", req)
	if err != nil {
		return nil, "", err
	}
	if code != http.StatusOK {
		return out, fmt.Sprintf("http_%d", code), nil
	}
	if out["error"] != nil {
		return out, "rpc_error", nil
	}
	res, _ := out["result"].(map[string]any)
	sc, _ := res["structuredContent"].(map[string]any)
	if res["isError"] == true {
		e, _ := sc["error"].(map[string]any)
		code, _ := e["code"].(string)
		if code == "" {
			code = "tool_error"
		}
		return sc, code, nil
	}
	return sc, "", nil
}

func (c *Client) MustTool(ctx context.Context, token, name string, args any) (map[string]any, error) {
	out, code, err := c.Tool(ctx, token, name, args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if code != "" {
		return nil, fmt.Errorf("%s failed: %s", name, code)
	}
	return out, nil
}

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, _ := cur.(map[string]any)
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

func list(m map[string]any, key string) []map[string]any {
	xs, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(xs))
	for _, x := range xs {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func num(m map[string]any, key string) float64 {
	f, _ := m[key].(float64)
	return f
}

type Agent struct {
	ID    string
	Token string
}

type Work struct {
	C       *Client
	BriefID string
	Leader  Agent
	Workers []Agent
	Tasks   []string
}

var seedSeq atomic.Int64

func (c *Client) Seed(ctx context.Context, workers int) (*Work, error) {
	prefix := fmt.Sprintf("gd%s%d", randHex(3), seedSeq.Add(1))
	w := &Work{C: c}
	reg := func(id, role, kind string) (Agent, error) {
		out, err := c.Must(ctx, http.MethodPost, "/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "gameday"}, http.StatusCreated)
		if err != nil {
			return Agent{}, err
		}
		return Agent{ID: id, Token: str(out, "connect", "token")}, nil
	}
	var err error
	if w.Leader, err = reg(prefix+"-lead", "leader", "other"); err != nil {
		return nil, err
	}
	for i := range workers {
		a, err := reg(fmt.Sprintf("%s-w%d", prefix, i), "worker", "claude-code")
		if err != nil {
			return nil, err
		}
		w.Workers = append(w.Workers, a)
	}
	brief, err := c.Must(ctx, http.MethodPost, "/briefs", map[string]any{
		"title": "gameday " + prefix, "body": "gameday scenario", "repos": []string{"gameday/repo"},
		"max_cost_eur": 100000, "max_parallel_agents": workers + 1,
	}, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	w.BriefID = str(brief, "id")
	specs := make([]any, workers)
	for i := range specs {
		specs[i] = map[string]any{
			"title": fmt.Sprintf("gameday task %d", i), "spec": "gameday", "repo": "gameday/repo",
			"acceptance_criteria": []string{"ok"}, "budget": map[string]any{"max_minutes": 600, "max_cost_eur": 100000},
		}
	}
	res, err := c.MustTool(ctx, w.Leader.Token, "propose_plan", map[string]any{"brief_id": w.BriefID, "tasks": specs})
	if err != nil {
		return nil, err
	}
	plan, _ := res["plan"].(map[string]any)
	if _, err := c.Must(ctx, http.MethodPost, "/plans/"+str(plan, "id")+"/approve", map[string]any{}, http.StatusOK); err != nil {
		return nil, err
	}
	for i, t := range list(plan, "tasks") {
		id := str(t, "id")
		if _, err := c.MustTool(ctx, w.Leader.Token, "assign", map[string]any{"task_id": id, "agent_id": w.Workers[i].ID}); err != nil {
			return nil, err
		}
		w.Tasks = append(w.Tasks, id)
	}
	return w, nil
}

func (w *Work) ClaimAll(ctx context.Context) error {
	for i, a := range w.Workers {
		if _, err := w.C.MustTool(ctx, a.Token, "claim_task", map[string]any{"task_id": w.Tasks[i]}); err != nil {
			return err
		}
	}
	return nil
}

func (w *Work) Cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = w.C.REST(ctx, http.MethodPatch, "/briefs/"+w.BriefID, map[string]any{"status": "closed"})
	for _, a := range append([]Agent{w.Leader}, w.Workers...) {
		_, _, _ = w.C.REST(ctx, http.MethodPost, "/agents/"+a.ID+"/revoke", map[string]any{"reason": "gameday cleanup"})
	}
}

func (w *Work) Task(ctx context.Context, i int) (map[string]any, error) {
	return w.C.Must(ctx, http.MethodGet, "/tasks/"+w.Tasks[i], nil, http.StatusOK)
}

func (c *Client) AuditOK(ctx context.Context) (bool, string, error) {
	out, err := c.Must(ctx, http.MethodGet, "/audit/verify", nil, http.StatusOK)
	if err != nil {
		return false, "", err
	}
	return out["ok"] == true, fmt.Sprintf("%v events", out["events"]), nil
}
