package client_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

func TestServerErrorCodes(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx := e.ctx
	tm := e.team()
	br := e.brief()
	loose := must(tm.leader.CreateTask(ctx, taskInput(br.ID, "loose")))
	reg := must(e.owner.RegisterAgent(ctx, board.AgentInput{ID: "zed", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}))

	cases := []struct {
		name   string
		err    error
		code   string
		status int
	}{
		{"owner brief not found", second(e.owner.GetBrief(ctx, "B-999")), client.CodeNotFound, 404},
		{"owner mark done conflict", second(e.owner.MarkDone(ctx, loose.ID, "")), client.CodeConflict, 409},
		{"owner bad request", second(e.owner.RegisterAgent(ctx, board.AgentInput{ID: "Bad ID", Role: model.RoleWorker})), client.CodeBadRequest, 400},
		{"owner bad token", second(client.NewOwner(e.url, "wrong-owner-token-wrong-owner-token").State(ctx)), client.CodeUnauthorized, 401},
		{"agent token on rest", second(client.NewOwner(e.url, reg.Connect.Token).State(ctx)), client.CodeForbidden, 403},
		{"owner token on mcp", client.NewMCP(e.url+"/mcp", ownerToken).Initialize(ctx), client.CodeForbidden, 403},
		{"bogus token on mcp", client.NewMCP(e.url+"/mcp", "ssp_bogus").Initialize(ctx), client.CodeUnauthorized, 401},
		{"tool task not found", second(tm.worker.GetTask(ctx, "T-999")), client.CodeNotFound, 0},
		{"tool not assigned", second(tm.worker.ClaimTask(ctx, loose.ID)), client.CodeNotAssigned, 0},
		{"tool bad request", second(tm.leader.CreateTask(ctx, board.TaskInput{BriefID: br.ID})), client.CodeBadRequest, 0},
		{"tool outside role", second(tm.worker.Board(ctx, "")), "rpc-32601", 0},
	}
	for _, c := range cases {
		var ce *client.Error
		if !errors.As(c.err, &ce) {
			t.Errorf("%s: err = %v, want *client.Error", c.name, c.err)
			continue
		}
		if ce.Code != c.code || ce.Status != c.status || ce.Message == "" {
			t.Errorf("%s: got %+v, want code %s status %d", c.name, ce, c.code, c.status)
		}
		if !client.IsCode(fmt.Errorf("wrapped: %w", c.err), c.code) || client.StatusOf(c.err) != c.status {
			t.Errorf("%s: helpers disagree with %+v", c.name, ce)
		}
	}
}

func TestPauseCapacityAndLoopCodes(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx := e.ctx
	tm := e.team()
	br := e.brief()
	task := e.readyTask(tm.leader, br.ID, "t")
	_ = must(tm.leader.Assign(ctx, task.ID, "wally", ""))

	if err := e.owner.PauseAll(ctx, "maintenance"); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.worker.ClaimTask(ctx, task.ID); !client.IsCode(err, client.CodePaused) {
		t.Fatalf("claim while paused: %v", err)
	}
	if hb := must(tm.worker.Heartbeat(ctx, "")); !hb.Paused {
		t.Fatalf("heartbeat while paused: %+v", hb)
	}
	if err := e.owner.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}

	_ = must(e.owner.SetCapacity(ctx, capacity.Config{GlobalMaxAgents: 1}))
	_, err := tm.leader.RequestSpawn(ctx, board.SpawnInput{BriefID: br.ID, Kind: model.KindClaudeCode, Model: "m", N: 20})
	if !client.IsCode(err, client.CodeCapacity) {
		t.Fatalf("spawn over capacity: %v", err)
	}

	for i := 1; i <= 6; i++ {
		_, err = tm.worker.AddNote(ctx, task.ID, "same note")
		if i < 6 && err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	if !client.IsCode(err, client.CodeAgentPaused) {
		t.Fatalf("sixth identical note: %v", err)
	}
	if a := must(e.owner.GetAgent(ctx, "wally")); a.Status != model.AgentPaused {
		t.Fatalf("agent status %s", a.Status)
	}
	if _, err := tm.worker.ClaimTask(ctx, task.ID); !client.IsCode(err, client.CodeAgentPaused) {
		t.Fatalf("claim while agent paused: %v", err)
	}
	_ = must(e.owner.ResumeAgent(ctx, "wally"))
	_ = must(tm.worker.ClaimTask(ctx, task.ID))
}

func TestPlainHTTPErrorsMapToCodes(t *testing.T) {
	want := map[int]string{
		400: client.CodeBadRequest, 401: client.CodeUnauthorized, 403: client.CodeForbidden, 404: client.CodeNotFound,
		409: client.CodeConflict, 423: client.CodePaused, 429: client.CodeRateLimited, 500: client.CodeInternal, 502: client.CodeInternal,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, _ := strconv.Atoi(strings.Split(r.URL.Path, "/")[1])
		http.Error(w, "plain failure", status)
	}))
	defer ts.Close()
	for status, code := range want {
		_, err := client.NewOwner(ts.URL+fmt.Sprintf("/%d", status), "t").State(t.Context())
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Status != status || ce.Code != code {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
