package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/callback"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type callbackReceiver struct {
	srv     *httptest.Server
	mu      sync.Mutex
	secrets map[string]string
	events  chan callback.Event
	bodies  chan string
}

func newCallbackReceiver(t *testing.T) *callbackReceiver {
	t.Helper()
	r := &callbackReceiver{secrets: map[string]string{}, events: make(chan callback.Event, 64), bodies: make(chan string, 64)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		agent := strings.TrimPrefix(req.URL.Path, "/")
		r.mu.Lock()
		secret := r.secrets[agent]
		r.mu.Unlock()
		ts, _ := strconv.ParseInt(req.Header.Get(callback.HeaderTimestamp), 10, 64)
		if err := callback.Verify(secret, ts, body, req.Header.Get(callback.HeaderSignature), time.Now(), time.Minute); err != nil {
			t.Errorf("agent %s: %v", agent, err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var ev callback.Event
		_ = json.Unmarshal(body, &ev)
		r.events <- ev
		r.bodies <- string(body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *callbackReceiver) register(t *testing.T, e *servertest.Env, id, role, kind string) string {
	t.Helper()
	code, out := e.REST("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m", "callback_url": r.srv.URL + "/" + id})
	if code != 201 {
		t.Fatalf("register %s: %d %v", id, code, out)
	}
	cb := out["callback"].(map[string]any)
	r.mu.Lock()
	r.secrets[id] = cb["secret"].(string)
	r.mu.Unlock()
	return out["connect"].(map[string]any)["token"].(string)
}

func (r *callbackReceiver) expect(t *testing.T, agent, reason string) callback.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-r.events:
			if ev.AgentID != agent {
				t.Fatalf("event for %s, want %s: %+v", ev.AgentID, agent, ev)
			}
			for _, rs := range ev.Reasons {
				if rs.Type == reason {
					return ev
				}
			}
			t.Fatalf("event for %s lacks reason %s: %+v", agent, reason, ev)
		case <-deadline:
			t.Fatalf("no %s wake-up for %s", reason, agent)
		}
	}
}

func (r *callbackReceiver) quiet(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("unexpected wake-up %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func startCallbackEnv(t *testing.T) *servertest.Env {
	t.Helper()
	t.Setenv("SUBSPACE_CALLBACK_ALLOW_PRIVATE", "1")
	t.Setenv("SUBSPACE_CALLBACK_COALESCE", "20ms")
	t.Setenv("SUBSPACE_CALLBACK_BACKOFF", "20ms")
	return servertest.New(t)
}

func planWithTask(t *testing.T, e *servertest.Env, lead string) string {
	t.Helper()
	code, brief := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, brief)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": brief["id"], "tasks": []any{
		map[string]any{"title": "Confidential title", "spec": "Confidential spec", "repo": "o/r", "acceptance_criteria": []string{"works"}},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	return plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
}

func TestCallbackWakesAgentsAcrossTheTaskLifecycle(t *testing.T) {
	e := startCallbackEnv(t)
	rcv := newCallbackReceiver(t)
	lead := e.Register("rex", "leader", "other")
	worker := rcv.register(t, e, "w1", "worker", "claude-code")
	rev := rcv.register(t, e, "r1", "reviewer", "codex")
	task := planWithTask(t, e, lead)

	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	ev := rcv.expect(t, "w1", callback.ReasonAssigned)
	if ev.Reasons[0].TaskID != task || ev.Type != callback.EventWake || ev.ID == "" {
		t.Fatalf("event %+v", ev)
	}
	body := <-rcv.bodies
	for _, secretText := range []string{"Confidential", "spec", "o/r", "works"} {
		if strings.Contains(body, secretText) {
			t.Fatalf("payload leaks task content %q: %s", secretText, body)
		}
	}
	rcv.quiet(t)

	if code, out := e.REST("POST", "/api/v1/threads/agent/w1/messages", map[string]any{"body": "please hurry"}); code != 200 && code != 201 {
		t.Fatalf("owner message: %d %v", code, out)
	}
	rcv.expect(t, "w1", callback.ReasonMessage)
	<-rcv.bodies

	e.Must(worker, "claim_task", map[string]any{"task_id": task})
	q := e.Must(worker, "ask_owner", map[string]any{"task_id": task, "question": "which db?"})["question"].(map[string]any)
	rcv.quiet(t)
	if code, out := e.REST("POST", "/api/v1/questions/"+q["id"].(string)+"/answer", map[string]any{"answer": "sqlite"}); code != 200 {
		t.Fatalf("answer: %d %v", code, out)
	}
	ev = rcv.expect(t, "w1", callback.ReasonAnswer)
	if ev.Reasons[0].QuestionID != q["id"] {
		t.Fatalf("event %+v", ev)
	}
	<-rcv.bodies
	rcv.quiet(t)

	e.Must(worker, "submit", map[string]any{"task_id": task, "pr_url": "https://example.com/o/r/pull/1", "summary": "done"})
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "r1", "as": "reviewer"})
	rcv.expect(t, "r1", callback.ReasonAssigned)
	<-rcv.bodies
	e.Must(rev, "review", map[string]any{"task_id": task, "verdict": "changes_requested", "comments": "tighten"})
	ev = rcv.expect(t, "w1", callback.ReasonChangesRequested)
	if ev.Reasons[0].TaskID != task {
		t.Fatalf("event %+v", ev)
	}
}

func TestCallbackIgnoresLeaderMessagesToOtherAgentsAndTaskThreadsOfStrangers(t *testing.T) {
	e := startCallbackEnv(t)
	rcv := newCallbackReceiver(t)
	lead := e.Register("rex", "leader", "other")
	rcv.register(t, e, "w1", "worker", "claude-code")
	rcv.register(t, e, "w2", "worker", "claude-code")
	task := planWithTask(t, e, lead)
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	rcv.expect(t, "w1", callback.ReasonAssigned)

	e.Must(lead, "post_message", map[string]any{"thread": "task:" + task, "body": "status?"})
	ev := rcv.expect(t, "w1", callback.ReasonMessage)
	if ev.Reasons[0].Thread != "task:"+task {
		t.Fatalf("event %+v", ev)
	}
	e.Must(lead, "post_message", map[string]any{"thread": "agent:w2", "body": "hello"})
	rcv.expect(t, "w2", callback.ReasonMessage)
	rcv.quiet(t)
}

func TestCallbackFailureNeverBlocksBoardOperations(t *testing.T) {
	e := startCallbackEnv(t)
	rcv := newCallbackReceiver(t)
	lead := e.Register("rex", "leader", "other")
	rcv.register(t, e, "w1", "worker", "claude-code")
	rcv.srv.Close()
	task := planWithTask(t, e, lead)
	start := time.Now()
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": "ping"})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("board operations took %s with a dead receiver", time.Since(start))
	}
	if code, out := e.REST("GET", "/api/v1/tasks/"+task, nil); code != 200 || out["status"] != "assigned" {
		t.Fatalf("task %d %v", code, out)
	}
}

func TestCallbackRegistrationRules(t *testing.T) {
	e := servertest.New(t)
	register := func(id string, extra map[string]any) (int, map[string]any) {
		body := map[string]any{"id": id, "role": "worker", "kind": "claude-code", "model": "m"}
		for k, v := range extra {
			body[k] = v
		}
		return e.REST("POST", "/api/v1/agents", body)
	}
	for i, bad := range []any{
		"http://example.com/hook", "https://127.0.0.1/hook", "https://localhost/hook", "https://10.1.2.3/hook",
		"https://169.254.169.254/latest/meta-data", "https://[::1]/hook", "https://[fd00:ec2::254]/", "https://user:pw@93.184.216.34/",
		"ftp://93.184.216.34/", "not a url", 42, []string{"https://93.184.216.34/"},
	} {
		id := "bad" + strconv.Itoa(i)
		if code, out := register(id, map[string]any{"callback_url": bad}); code != 400 {
			t.Errorf("%v accepted: %d %v", bad, code, out)
		}
		if code, _ := e.REST("GET", "/api/v1/agents/"+id, nil); code != 404 {
			t.Errorf("agent %s was created despite the bad callback_url", id)
		}
	}

	code, out := register("ok1", map[string]any{"callback_url": "https://93.184.216.34/hook"})
	if code != 201 {
		t.Fatalf("register: %d %v", code, out)
	}
	first := out["callback"].(map[string]any)
	if first["url"] != "https://93.184.216.34/hook" || !strings.HasPrefix(first["secret"].(string), "sscb_") || out["connect"].(map[string]any)["token"] == "" {
		t.Fatalf("response %v", out)
	}

	code, out = e.REST("POST", "/api/v1/agents/ok1/rotate-token", map[string]any{"callback_url": "https://93.184.216.35/other"})
	if code != 200 || out["callback"].(map[string]any)["secret"] == first["secret"] || out["connect"].(map[string]any)["token"] == "" {
		t.Fatalf("rotate: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/agents/ok1/rotate-token", map[string]any{})
	if _, present := out["callback"]; code != 200 || present {
		t.Fatalf("plain rotate must not touch the callback: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/agents/ok1/rotate-token", map[string]any{"callback_url": ""})
	if v, present := out["callback"]; code != 200 || !present || v != nil {
		t.Fatalf("clear: %d %v", code, out)
	}
	if code, _ := e.REST("POST", "/api/v1/agents/nobody/rotate-token", map[string]any{"callback_url": "https://93.184.216.34/"}); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}
	if code, out := register("plain", nil); code != 201 || out["callback"] != nil {
		t.Fatalf("plain register: %d %v", code, out)
	}
}

func TestCallbackRegistrationRequiresOwner(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("w1", "worker", "claude-code")
	body, _ := json.Marshal(map[string]any{"id": "w2", "role": "worker", "kind": "claude-code", "callback_url": "https://93.184.216.34/"})
	for _, auth := range []string{"Bearer " + tok, "", "Bearer nope"} {
		req, _ := http.NewRequest("POST", e.TS.URL+"/api/v1/agents", strings.NewReader(string(body)))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Fatalf("auth %q: %d", auth, resp.StatusCode)
		}
	}
	if code, _ := e.REST("GET", "/api/v1/agents/w2", nil); code != 404 {
		t.Fatal("agent created without owner auth")
	}
}
