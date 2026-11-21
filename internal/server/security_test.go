package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func (e *env) restRaw(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errMessage(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	if e == nil {
		return ""
	}
	m, _ := e["message"].(string)
	return m
}

func TestRESTDecodeDoesNotLeakTypeInfo(t *testing.T) {
	e := newEnv(t)

	code, out := e.restRaw("POST", "/api/v1/briefs", `{"max_cost_eur":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", code, out)
	}
	for _, leak := range []string{"Go struct field", "BriefInput", "float64", "of type"} {
		if strings.Contains(errMessage(out), leak) {
			t.Fatalf("error message leaks internals: %q in %q", leak, errMessage(out))
		}
	}

	code, out = e.restRaw("POST", "/api/v1/briefs", `{"title":`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", code, out)
	}
	if msg := errMessage(out); strings.Contains(msg, "unexpected end of JSON") || strings.Contains(msg, "Go struct field") {
		t.Fatalf("syntax error leaks internals: %q", msg)
	}
}

func TestMCPDecodeDoesNotLeakTypeInfo(t *testing.T) {
	e := newEnv(t)
	wk := e.register("w1", "worker", "claude-code")

	res := e.rpc(wk, "tools/call", map[string]any{"name": "get_task", "arguments": map[string]any{"task_id": 123}})
	if res["error"] != nil {
		t.Fatalf("unexpected rpc error: %v", res["error"])
	}
	r := res["result"].(map[string]any)
	sc, _ := r["structuredContent"].(map[string]any)
	if r["isError"] != true {
		t.Fatalf("expected tool error, got %v", r)
	}
	msg, _ := sc["error"].(map[string]any)["message"].(string)
	for _, leak := range []string{"Go struct field", "of type", "int", "TaskID"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("tool error leaks internals: %q in %q", leak, msg)
		}
	}
	if msg != "invalid arguments" {
		t.Fatalf("message = %q, want generic", msg)
	}
}

func TestRESTListLimitClamped(t *testing.T) {
	e := newEnv(t)
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)

	err := e.srv.Store.Atomic(context.Background(), func(tx store.Tx) error {
		for i := 1; i <= 1100; i++ {
			at := time.Now().UTC()
			if err := tx.InsertTask(&model.Task{ID: fmt.Sprintf("T-%d", i), BriefID: bid, PlanID: "P-1", Title: "t", Spec: "s", Repo: "o/r",
				Role: model.RoleWorker, Status: model.StatusReady, AcceptanceCriteria: []string{}, DependsOn: []string{}, CreatedAt: at, UpdatedAt: at}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out := e.rest("GET", "/api/v1/tasks?limit=99999999", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%v)", code, out)
	}
	ts := out["tasks"].([]any)
	if len(ts) != 1000 {
		t.Fatalf("limit not clamped: got %d tasks, want 1000", len(ts))
	}
}
