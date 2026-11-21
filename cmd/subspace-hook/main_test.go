package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
)

const (
	ownerToken = "owner-token-for-hook-tests-0123456789"
	repo       = "SigmaUno/hook-test"
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	url    string
	token  string
	taskID string
	owner  *client.Owner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(t.TempDir(), "hook.db"), OwnerToken: ownerToken, PublicURL: "http://sub.test",
		FlushEvery: 20 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })

	f := &fixture{t: t, ctx: ctx, url: ts.URL, owner: client.NewOwner(ts.URL, ownerToken)}
	mcp := func(token string) *client.MCP {
		c := client.NewMCP(ts.URL+"/mcp", token)
		if err := c.Initialize(ctx); err != nil {
			t.Fatal(err)
		}
		return c
	}
	reg := func(id, role, kind string) *client.MCP {
		r, err := f.owner.RegisterAgent(ctx, board.AgentInput{ID: id, Role: role, Kind: kind, Model: "test-model"})
		if err != nil {
			t.Fatal(err)
		}
		if id == "cody" {
			f.token = r.Connect.Token
		}
		return mcp(r.Connect.Token)
	}
	leader := reg("rex", model.RoleLeader, model.KindOther)
	worker := reg("cody", model.RoleWorker, model.KindCodex)

	br, err := f.owner.CreateBrief(ctx, board.BriefInput{Title: "hooks", Body: "body", Repos: []string{repo}, MaxCostEUR: 100, MaxParallel: 5})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := leader.ProposePlan(ctx, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{{
		BriefID: br.ID, Title: "wire hooks", Spec: "wire hooks", Repo: repo, AcceptanceCriteria: []string{"works"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.owner.ApprovePlan(ctx, plan.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.taskID = plan.Tasks[0].ID
	if _, err := leader.Assign(ctx, f.taskID, "cody", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ClaimTask(ctx, f.taskID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) hook(env map[string]string, stdin string, args ...string) (int, string) {
	f.t.Helper()
	var errb bytes.Buffer
	code := run(f.ctx, args, strings.NewReader(stdin), &errb, func(k string) string { return env[k] })
	return code, errb.String()
}

func (f *fixture) env() map[string]string {
	return map[string]string{"SUBSPACE_URL": f.url, "SUBSPACE_TOKEN": f.token, "SUBSPACE_TASK_ID": f.taskID}
}

func (f *fixture) task() *model.Task {
	f.t.Helper()
	tk, err := f.owner.GetTask(f.ctx, f.taskID)
	if err != nil {
		f.t.Fatal(err)
	}
	return tk
}

func (f *fixture) thread() []*model.Message {
	f.t.Helper()
	msgs, err := f.owner.ThreadMessages(f.ctx, "task", f.taskID, 0, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return msgs
}

func (f *fixture) output() []agentout.Line {
	f.t.Helper()
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodGet, f.url+"/api/v1/agents/cody/output", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out agentout.Output
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		f.t.Fatal(err)
	}
	return out.Lines
}

func hasMessage(msgs []*model.Message, kind, sub string) bool {
	for _, m := range msgs {
		if m.Kind == kind && strings.Contains(m.Body, sub) {
			return true
		}
	}
	return false
}

func TestHookSubcommandsAgainstServer(t *testing.T) {
	f := newFixture(t)
	before := f.task().LastHeartbeatAt

	if code, msg := f.hook(f.env(), "", "heartbeat"); code != 0 || msg != "" {
		t.Fatalf("heartbeat: %d %q", code, msg)
	}
	after := f.task().LastHeartbeatAt
	if after == nil || (before != nil && !after.After(*before)) {
		t.Fatalf("heartbeat not recorded: before=%v after=%v", before, after)
	}

	if code, msg := f.hook(f.env(), "", "status", "--status", "in_progress", "--progress", "40", "--note", "halfway"); code != 0 || msg != "" {
		t.Fatalf("status: %d %q", code, msg)
	}
	if got := f.task().ProgressPct; got != 40 {
		t.Fatalf("progress %d", got)
	}

	if code, msg := f.hook(f.env(), "", "note", "decided", "to", "use", "sqlite"); code != 0 || msg != "" {
		t.Fatalf("note: %d %q", code, msg)
	}
	if !hasMessage(f.thread(), "note", "decided to use sqlite") {
		t.Fatalf("note missing: %+v", f.thread())
	}

	event := `{"type":"agent-turn-complete","turn-id":"turn-7","last-assistant-message":"done"}`
	if code, msg := f.hook(f.env(), "", "heartbeat", "--event", event); code != 0 || msg != "" {
		t.Fatalf("heartbeat event: %d %q", code, msg)
	}
	if !hasMessage(f.thread(), "status", "codex turn complete turn-7") {
		t.Fatalf("turn status missing: %+v", f.thread())
	}
	msgs := len(f.thread())
	if code, _ := f.hook(f.env(), "", "heartbeat", "--event", `{"type":"approval-requested"}`); code != 0 {
		t.Fatal("unknown event must fail open")
	}
	if len(f.thread()) != msgs {
		t.Fatal("unknown event must only heartbeat")
	}

	if code, msg := f.hook(f.env(), "build ok\nall tests pass\n", "log", "--stream", "stdout", "--seq", "1"); code != 0 || msg != "" {
		t.Fatalf("log: %d %q", code, msg)
	}
	if code, msg := f.hook(f.env(), "retry of chunk one\n", "log", "--seq", "1"); code != 0 || msg != "" {
		t.Fatalf("log retry: %d %q", code, msg)
	}
	lines := f.output()
	if len(lines) != 2 || lines[0].Text != "build ok" || lines[1].Text != "all tests pass" || lines[0].TaskID == nil || *lines[0].TaskID != f.taskID {
		t.Fatalf("output %+v", lines)
	}
}

func TestHookTokenFileAndFailOpen(t *testing.T) {
	f := newFixture(t)

	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(f.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SUBSPACE_URL": f.url, "SUBSPACE_TOKEN_FILE": file}
	if code, msg := f.hook(env, "", "note", "from token file"); code != 0 || msg != "" {
		t.Fatalf("token file: %d %q", code, msg)
	}

	bad := f.env()
	bad["SUBSPACE_TOKEN"] = "ssp_" + strings.Repeat("x", 43)
	code, msg := f.hook(bad, "", "heartbeat")
	if code != 0 || msg == "" || strings.Contains(msg, bad["SUBSPACE_TOKEN"]) {
		t.Fatalf("bad token: %d %q", code, msg)
	}

	for name, env := range map[string]map[string]string{
		"no token": {"SUBSPACE_URL": f.url},
		"no url":   {"SUBSPACE_TOKEN": f.token},
	} {
		if code, msg := f.hook(env, "", "heartbeat"); code != 0 || msg == "" {
			t.Fatalf("%s: %d %q", name, code, msg)
		}
	}
}

func TestHookTimesOutAgainstHungServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })

	var errb bytes.Buffer
	env := map[string]string{"SUBSPACE_URL": srv.URL, "SUBSPACE_TOKEN": "ssp_hung", "SUBSPACE_TASK_ID": "T-1"}
	start := time.Now()
	code := run(context.Background(), []string{"heartbeat"}, strings.NewReader(""), &errb, func(k string) string { return env[k] })
	if code != 0 || errb.Len() == 0 {
		t.Fatalf("hung server: %d %q", code, errb.String())
	}
	if d := time.Since(start); d > callTimeout+time.Second {
		t.Fatalf("took %v", d)
	}
}
