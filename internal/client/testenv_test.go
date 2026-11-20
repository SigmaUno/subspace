package client_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
)

const (
	ownerToken = "owner-token-for-client-tests-0123456789"
	repo       = "SigmaUno/client-test"
)

type env struct {
	t     *testing.T
	ctx   context.Context
	url   string
	owner *client.Owner
	seen  *seenOps
}

type seenOps struct {
	mu  sync.Mutex
	ops map[string]bool
}

func (s *seenOps) observe(op string, _ time.Duration, _ error) {
	s.mu.Lock()
	s.ops[op] = true
	s.mu.Unlock()
}

func (s *seenOps) has(op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ops[op]
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(t.TempDir(), "client.db"), OwnerToken: ownerToken, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	seen := &seenOps{ops: map[string]bool{}}
	return &env{
		t: t, ctx: ctx, url: ts.URL, seen: seen,
		owner: client.NewOwner(ts.URL, ownerToken, client.WithObserver(seen.observe)),
	}
}

func (e *env) mcp(token string) *client.MCP {
	e.t.Helper()
	c := client.NewMCP(e.url+"/mcp", token, client.WithObserver(e.seen.observe))
	if err := c.Initialize(e.ctx); err != nil {
		e.t.Fatalf("initialize: %v", err)
	}
	return c
}

func (e *env) agent(id, role, kind string) *client.MCP {
	e.t.Helper()
	r, err := e.owner.RegisterAgent(e.ctx, board.AgentInput{ID: id, Role: role, Kind: kind, Model: "test-model"})
	if err != nil {
		e.t.Fatalf("register %s: %v", id, err)
	}
	return e.mcp(r.Connect.Token)
}

type team struct {
	leader, worker, tester, reviewer *client.MCP
}

func (e *env) team() team {
	e.t.Helper()
	return team{
		leader:   e.agent("rex", model.RoleLeader, model.KindOther),
		worker:   e.agent("wally", model.RoleWorker, model.KindClaudeCode),
		tester:   e.agent("tess", model.RoleTester, model.KindClaudeCode),
		reviewer: e.agent("rev", model.RoleReviewer, model.KindCodex),
	}
}

func (e *env) brief() *model.Brief {
	e.t.Helper()
	b, err := e.owner.CreateBrief(e.ctx, board.BriefInput{Title: "client brief", Body: "body", Repos: []string{repo}, MaxCostEUR: 100, MaxParallel: 5})
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func taskInput(briefID, title string) board.TaskInput {
	return board.TaskInput{BriefID: briefID, Title: title, Spec: "do " + title, Repo: repo, AcceptanceCriteria: []string{"works"}}
}

type failure struct {
	err  error
	file string
	line int
}

func must[T any](v T, err error) T {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		panic(failure{err, filepath.Base(file), line})
	}
	return v
}

func guard(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		f, ok := r.(failure)
		if !ok {
			panic(r)
		}
		t.Fatalf("%s:%d: %v", f.file, f.line, f.err)
	}
}

func (e *env) readyTask(leader *client.MCP, briefID, title string) *model.Task {
	e.t.Helper()
	p, err := leader.ProposePlan(e.ctx, board.PlanInput{BriefID: briefID, Tasks: []board.TaskInput{taskInput(briefID, title)}})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.owner.ApprovePlan(e.ctx, p.ID, ""); err != nil {
		e.t.Fatal(err)
	}
	return p.Tasks[0]
}
