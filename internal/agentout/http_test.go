package agentout_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func assignedTask(e *servertest.Env, lead, worker string) string {
	e.T.Helper()
	code, b := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "out", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	if code != 201 {
		e.T.Fatalf("brief: %d %v", code, b)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": b["id"], "tasks": []any{
		map[string]any{"title": "build", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"builds"}},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		e.T.Fatalf("approve: %d %v", code, out)
	}
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": worker})
	return tid
}

func lines(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, _ := out["lines"].([]any)
	res := make([]map[string]any, len(raw))
	for i, l := range raw {
		res[i] = l.(map[string]any)
	}
	return res
}

func TestLogOutputThroughMCPAndREST(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")
	tid := assignedTask(e, lead, "w1")

	res := e.Must(w1, "log_output", map[string]any{"task_id": tid, "stream": "stdout",
		"text": "\x1b]0;owned\x07\x1b[1;32mok\x1b[0m  pkg\x1b[2J\n"})
	if res["ok"] != true || res["lines"] != 1.0 || res["seq"] != 1.0 || res["truncated"] != false {
		t.Fatalf("log_output: %v", res)
	}
	e.Must(w1, "log_output", map[string]any{"task_id": tid, "stream": "stdout", "text": "\x1b[33mGITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123"})
	e.Must(w1, "log_output", map[string]any{"stream": "stderr", "text": "untagged"})
	if res := e.Must(w1, "log_output", map[string]any{"stream": "note", "text": strings.Repeat("z", 5000)}); res["truncated"] != true {
		t.Fatalf("oversized chunk: %v", res)
	}

	code, out := e.REST("GET", "/api/v1/agents/w1/output", nil)
	got := lines(t, out)
	if code != 200 || len(got) != 4 || out["last_seq"] != 4.0 || out["next_since"] != 4.0 || out["gap"] != false {
		t.Fatalf("output: %d %v", code, out)
	}
	if got[0]["text"] != "\x1b[1;32mok\x1b[0m  pkg" || got[0]["task_id"] != tid || got[0]["stream"] != "stdout" || got[0]["agent_id"] != "w1" {
		t.Fatalf("sanitized line: %v", got[0])
	}
	if txt := got[1]["text"].(string); strings.Contains(txt, "abcdefghij") || !strings.Contains(txt, "ghp_****") {
		t.Fatalf("secret not redacted: %q", txt)
	}
	if got[2]["task_id"] != nil || len(got[3]["text"].(string)) != 4096 {
		t.Fatalf("untagged and truncated lines: %v %d", got[2], len(got[3]["text"].(string)))
	}

	_, out = e.REST("GET", "/api/v1/agents/w1/output?since=1&limit=2", nil)
	if got := lines(t, out); len(got) != 2 || got[0]["seq"] != 2.0 || out["more"] != true || out["next_since"] != 3.0 {
		t.Fatalf("since page: %v", out)
	}
	_, out = e.REST("GET", "/api/v1/agents/w1/output?task_id="+tid, nil)
	if got := lines(t, out); len(got) != 2 {
		t.Fatalf("task filter: %v", out)
	}
	for _, q := range []string{"since=-1", "since=x", "limit=0"} {
		if code, out := e.REST("GET", "/api/v1/agents/w1/output?"+q, nil); code != 400 {
			t.Fatalf("%s: %d %v", q, code, out)
		}
	}
	if code, out := e.REST("GET", "/api/v1/agents/nobody/output", nil); code != 404 {
		t.Fatalf("unknown agent: %d %v", code, out)
	}

	e.MustFail(w2, "log_output", map[string]any{"task_id": tid, "stream": "stdout", "text": "spoof"}, "not_found")
	e.MustFail(w2, "log_output", map[string]any{"stream": "tty", "text": "x"}, "bad_request")
	e.MustFail(w2, "log_output", map[string]any{"stream": "stdout", "text": ""}, "bad_request")
	e.Must(lead, "log_output", map[string]any{"task_id": tid, "stream": "note", "text": "leader watching"})

	code, out = e.REST("GET", "/api/v1/agent-output", nil)
	agents, _ := out["agents"].([]any)
	if code != 200 || len(agents) != 2 {
		t.Fatalf("latest: %d %v", code, out)
	}
	newest := agents[0].(map[string]any)
	if newest["agent_id"] != "rex" || newest["lines"] != 1.0 || newest["line"].(map[string]any)["text"] != "leader watching" {
		t.Fatalf("latest order: %v", agents)
	}
	if code, _ := e.REST("POST", "/api/v1/agents/w1/revoke", map[string]any{}); code != 200 {
		t.Fatal("revoke failed")
	}
	if _, out := e.REST("GET", "/api/v1/agent-output", nil); len(out["agents"].([]any)) != 1 {
		t.Fatalf("revoked agent still listed: %v", out)
	}

	if _, out := e.REST("GET", "/api/v1/audit?tool=log_output", nil); len(out["events"].([]any)) != 0 {
		t.Fatalf("log_output audited: %v", out)
	}
	_, out = e.REST("GET", "/api/v1/threads/task/"+tid+"/messages", nil)
	for _, m := range out["messages"].([]any) {
		if body := m.(map[string]any)["body"].(string); strings.Contains(body, "pkg") || strings.Contains(body, "leader watching") {
			t.Fatalf("output leaked into thread: %q", body)
		}
	}
}

func TestRepeatedOutputDoesNotTripLoopProtection(t *testing.T) {
	e := servertest.New(t)
	w := e.Register("w1", "worker", "claude-code")
	for range 30 {
		e.Must(w, "log_output", map[string]any{"stream": "stdout", "text": "waiting for lock..."})
	}
	if a := e.Must(w, "whoami", map[string]any{})["agent"].(map[string]any); a["status"] != "active" {
		t.Fatalf("agent paused by identical output lines: %v", a)
	}
	seq := 7
	e.Must(w, "log_output", map[string]any{"stream": "stdout", "text": "chunk", "seq": seq})
	for range 3 {
		if res := e.Must(w, "log_output", map[string]any{"stream": "stdout", "text": "chunk", "seq": seq}); res["duplicate"] != true || res["seq"] != 31.0 {
			t.Fatalf("retry: %v", res)
		}
	}
}

func TestOutputRateLimit(t *testing.T) {
	t.Setenv("SUBSPACE_OUTPUT_RATE", "1/m:3")
	e := servertest.New(t)
	w := e.Register("w1", "worker", "claude-code")
	other := e.Register("w2", "worker", "claude-code")
	for i := range 3 {
		if _, isErr := e.Call(w, "log_output", map[string]any{"stream": "stdout", "text": "line"}); isErr {
			t.Fatalf("call %d limited", i)
		}
	}
	e.MustFail(w, "log_output", map[string]any{"stream": "stdout", "text": "line"}, "rate_limited")
	e.Must(other, "log_output", map[string]any{"stream": "stdout", "text": "line"})
	if a := e.Must(w, "whoami", map[string]any{})["agent"].(map[string]any); a["status"] != "active" {
		t.Fatalf("rate limited agent paused: %v", a)
	}
}

type sse struct {
	resp *http.Response
	ch   chan [2]string
}

func openSSE(t *testing.T, e *servertest.Env, tok, topics string) *sse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics="+topics, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	s := &sse{resp: resp, ch: make(chan [2]string, 64)}
	if resp.StatusCode != 200 {
		return s
	}
	go func() {
		defer close(s.ch)
		br := bufio.NewReader(resp.Body)
		var typ string
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimRight(l, "\n")
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				typ = v
			} else if v, ok := strings.CutPrefix(l, "data: "); ok {
				s.ch <- [2]string{typ, v}
			}
		}
	}()
	return s
}

func (s *sse) next(t *testing.T, want string) map[string]any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.ch:
			if !ok {
				t.Fatalf("stream ended waiting for %s", want)
			}
			if ev[0] == "hello" && want != "hello" {
				continue
			}
			if ev[0] != want {
				t.Fatalf("got %s %s, want %s", ev[0], ev[1], want)
			}
			var env struct{ Data map[string]any }
			if err := json.Unmarshal([]byte(ev[1]), &env); err != nil {
				t.Fatal(err)
			}
			return env.Data
		case <-timeout:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestOutputStream(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")
	w3 := e.Register("w3", "worker", "claude-code")
	tid := assignedTask(e, lead, "w1")

	if code, _ := e.REST("GET", "/api/v1/events?topics=output:w1,board", nil); code != 400 {
		t.Fatalf("mixed topics: %d", code)
	}
	for tok, want := range map[string]int{"": 401, w1: 403} {
		if s := openSSE(t, e, tok, "output:w1"); s.resp.StatusCode != want {
			t.Fatalf("token %q: %d want %d", tok, s.resp.StatusCode, want)
		}
	}

	follow := openSSE(t, e, servertest.OwnerToken, "output:w1,output:w2")
	every := openSSE(t, e, servertest.OwnerToken, "output")
	main := openSSE(t, e, servertest.OwnerToken, "all")
	follow.next(t, "hello")
	every.next(t, "hello")
	main.next(t, "hello")

	e.Must(w3, "log_output", map[string]any{"stream": "stdout", "text": "not followed"})
	e.Must(w1, "log_output", map[string]any{"task_id": tid, "stream": "stdout", "text": "\x1b[31mfail\x1b[0m\x1b]0;t\x07\nnext"})
	e.Must(w2, "log_output", map[string]any{"stream": "stderr", "text": "w2 line"})

	got := follow.next(t, "agent.output")
	ls := got["lines"].([]any)
	if got["agent_id"] != "w1" || got["task_id"] != tid || len(ls) != 2 || ls[0].(map[string]any)["text"] != "\x1b[31mfail\x1b[0m" {
		t.Fatalf("w1 chunk: %v", got)
	}
	if got := follow.next(t, "agent.output"); got["agent_id"] != "w2" {
		t.Fatalf("w2 chunk: %v", got)
	}
	for _, want := range []string{"w3", "w1", "w2"} {
		if got := every.next(t, "agent.output"); got["agent_id"] != want {
			t.Fatalf("output topic: got %v want %s", got["agent_id"], want)
		}
	}

	e.Must(lead, "post_message", map[string]any{"thread": "task:" + tid, "body": "marker"})
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-main.ch:
			if !ok {
				t.Fatal("main stream ended")
			}
			if ev[0] == "agent.output" {
				t.Fatalf("agent output on the main stream: %s", ev[1])
			}
			if ev[0] == "message.created" && strings.Contains(ev[1], "marker") {
				return
			}
		case <-timeout:
			t.Fatal("marker message never arrived")
		}
	}
}

func TestOutputTailSurvivesRestart(t *testing.T) {
	t.Setenv("SUBSPACE_OUTPUT_PERSIST", "3")
	cfg := server.Config{PublicURL: "http://sub.test", DBPath: servertest.DB(t),
		FlushEvery: 20 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
	a := servertest.Start(t, cfg)
	w := a.Register("w1", "worker", "claude-code")
	for _, text := range []string{"one", "two\nthree", "four\nfive"} {
		a.Must(w, "log_output", map[string]any{"stream": "stdout", "text": text})
	}
	time.Sleep(300 * time.Millisecond)

	b := servertest.Start(t, cfg)
	_, out := b.REST("GET", "/api/v1/agents/w1/output", nil)
	got := lines(t, out)
	if len(got) != 3 || got[0]["text"] != "three" || got[0]["seq"] != 3.0 || got[2]["text"] != "five" || out["last_seq"] != 5.0 {
		t.Fatalf("restored tail: %v", out)
	}
	if res := b.Must(w, "log_output", map[string]any{"stream": "stdout", "text": "six"}); res["seq"] != 6.0 {
		t.Fatalf("seq after restart: %v", res)
	}
}

func TestIdleTailOfRevokedAgentIsDropped(t *testing.T) {
	t.Setenv("SUBSPACE_OUTPUT_PERSIST", "5")
	var offset atomic.Int64
	cfg := server.Config{PublicURL: "http://sub.test", DBPath: servertest.DB(t),
		FlushEvery: 20 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Hour,
		Now: func() time.Time { return time.Now().UTC().Add(time.Duration(offset.Load())) }}
	a := servertest.Start(t, cfg)
	gone := a.Register("w1", "worker", "claude-code")
	kept := a.Register("w2", "worker", "claude-code")
	for _, tok := range []string{gone, kept} {
		a.Must(tok, "log_output", map[string]any{"stream": "stdout", "text": "one\ntwo"})
	}
	if code, out := a.REST("POST", "/api/v1/agents/w1/revoke", map[string]any{"reason": "done"}); code != 200 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	time.Sleep(200 * time.Millisecond)
	offset.Store(int64(agentout.IdleTTL + 2*time.Minute))

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out := a.REST("GET", "/api/v1/agents/w1/output", nil)
		if len(lines(t, out)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("revoked agent tail kept: %v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, out := a.REST("GET", "/api/v1/agents/w2/output", nil); len(lines(t, out)) != 2 {
		t.Fatalf("active agent tail dropped: %v", out)
	}

	b := servertest.Start(t, cfg)
	if _, out := b.REST("GET", "/api/v1/agents/w1/output", nil); len(lines(t, out)) != 0 {
		t.Fatalf("revoked agent tail restored after restart: %v", out)
	}
	if _, out := b.REST("GET", "/api/v1/agents/w2/output", nil); len(lines(t, out)) != 2 {
		t.Fatalf("active agent tail lost after restart: %v", out)
	}
}
