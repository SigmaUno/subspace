package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type pushFrame struct {
	id, event string
	data      string
}

func (f pushFrame) rpc(t *testing.T) (method string, params map[string]any) {
	t.Helper()
	var m struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(f.data), &m); err != nil {
		t.Fatalf("frame %+v: %v", f, err)
	}
	return m.Method, m.Params
}

type pushStream struct {
	frames <-chan pushFrame
	cancel context.CancelFunc
}

func mcpInit(t *testing.T, e *servertest.Env, tok string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatalf("initialize: %d without session", resp.StatusCode)
	}
	return sid
}

func mcpRPC(t *testing.T, e *servertest.Env, tok, sid, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func pushGet(e *servertest.Env, ctx context.Context, tok, sid, lastID, accept string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.TS.URL+"/mcp", nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return http.DefaultClient.Do(req)
}

func openPush(t *testing.T, e *servertest.Env, tok, sid, lastID string) *pushStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := pushGet(e, ctx, tok, sid, lastID, "text/event-stream")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		_ = resp.Body.Close()
		t.Fatalf("GET /mcp: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan pushFrame, 4096)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		var f pushFrame
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f != (pushFrame{}) {
					out <- f
				}
				f = pushFrame{}
			case strings.HasPrefix(line, ": "):
				out <- pushFrame{event: "comment", data: line[2:]}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	t.Cleanup(cancel)
	return &pushStream{frames: out, cancel: cancel}
}

func (s *pushStream) next(t *testing.T, within time.Duration) pushFrame {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("stream ended")
		}
		return f
	case <-time.After(within):
		t.Fatal("no frame")
	}
	return pushFrame{}
}

func (s *pushStream) event(t *testing.T, typ string) map[string]any {
	t.Helper()
	f := s.next(t, 3*time.Second)
	method, params := f.rpc(t)
	if method != "notifications/subspace/event" || params["type"] != typ {
		t.Fatalf("want event %s, got %s %v", typ, method, params)
	}
	return params
}

func (s *pushStream) updatedURIs(t *testing.T) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				t.Fatal("stream ended")
			}
			if method, p := f.rpc(t); method == "notifications/resources/updated" {
				got[p["uri"].(string)] = true
			}
		case <-time.After(300 * time.Millisecond):
			return got
		}
	}
}

func (s *pushStream) quiet(t *testing.T) {
	t.Helper()
	select {
	case f := <-s.frames:
		t.Fatalf("unexpected frame %+v", f)
	case <-time.After(150 * time.Millisecond):
	}
}

func (s *pushStream) ended(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-s.frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream still open")
		}
	}
}

func pushFixture(t *testing.T) (e *servertest.Env, lead, worker, wsid string, task string, st *pushStream) {
	t.Helper()
	e = servertest.New(t)
	lead = e.Register("rex", "leader", "other")
	worker = e.Register("w1", "worker", "claude-code")
	task = planWithTask(t, e, lead)
	wsid = mcpInit(t, e, worker)
	st = openPush(t, e, worker, wsid, "")
	return
}

func TestMCPPushAssignmentWakesIdleAgent(t *testing.T) {
	e, lead, _, _, task, st := pushFixture(t)
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	start := time.Now()
	f := st.next(t, 100*time.Millisecond)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("wake took %s", d)
	}
	method, params := f.rpc(t)
	if f.event != "message" || f.id == "" || method != "notifications/subspace/event" || params["type"] != "task_assigned" || params["task_id"] != task {
		t.Fatalf("frame %+v %v", f, params)
	}
	if len(params) != 2 {
		t.Fatalf("event carries more than ids: %v", params)
	}
	st.quiet(t)
}

func TestMCPPushEventsKeepOrderAndCarryIDsOnly(t *testing.T) {
	e, lead, worker, _, task, st := pushFixture(t)
	rev := e.Register("r1", "reviewer", "codex")

	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	st.event(t, "task_assigned")
	if code, out := e.REST("POST", "/api/v1/threads/agent/w1/messages", map[string]any{"body": "please hurry"}); code != 200 && code != 201 {
		t.Fatalf("owner message: %d %v", code, out)
	}
	if p := st.event(t, "message"); p["thread"] != "agent:w1" || p["message_id"] == nil {
		t.Fatalf("message event %v", p)
	}
	e.Must(lead, "post_message", map[string]any{"thread": "task:" + task, "body": "status?"})
	if p := st.event(t, "message"); p["task_id"] != task {
		t.Fatalf("task message event %v", p)
	}

	e.Must(worker, "claim_task", map[string]any{"task_id": task})
	q := e.Must(worker, "ask_owner", map[string]any{"task_id": task, "question": "which db?"})["question"].(map[string]any)
	st.quiet(t)
	if code, out := e.REST("POST", "/api/v1/questions/"+q["id"].(string)+"/answer", map[string]any{"answer": "sqlite"}); code != 200 {
		t.Fatalf("answer: %d %v", code, out)
	}
	if p := st.event(t, "answer"); p["question_id"] != q["id"] || p["task_id"] != task {
		t.Fatalf("answer event %v", p)
	}

	e.Must(worker, "submit", map[string]any{"task_id": task, "pr_url": "https://example.com/o/r/pull/1", "summary": "done"})
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "r1", "as": "reviewer"})
	e.Must(rev, "review", map[string]any{"task_id": task, "verdict": "changes_requested", "comments": "tighten"})
	if p := st.event(t, "changes_requested"); p["task_id"] != task {
		t.Fatalf("changes_requested event %v", p)
	}

	if code, out := e.REST("POST", "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"w1"}, "action": "pause"}); code != 200 {
		t.Fatalf("pause: %d %v", code, out)
	}
	st.event(t, "paused")
	if code, out := e.REST("POST", "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"w1"}, "action": "resume"}); code != 200 {
		t.Fatalf("resume: %d %v", code, out)
	}
	st.event(t, "resumed")
	st.quiet(t)

	if code, _ := e.REST("POST", "/api/v1/pause-all", map[string]any{"reason": "x"}); code != 200 {
		t.Fatal("pause-all")
	}
	if p := st.event(t, "paused"); p["scope"] != "all" {
		t.Fatalf("global pause event %v", p)
	}
}

func TestMCPPushFrameIDsAscend(t *testing.T) {
	e, lead, _, _, task, st := pushFixture(t)
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	for i := range 5 {
		e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": fmt.Sprint("m", i)})
	}
	last := int64(0)
	for range 6 {
		f := st.next(t, 3*time.Second)
		id, err := strconv.ParseInt(f.id, 10, 64)
		if err != nil || id != last+1 {
			t.Fatalf("frame id %q after %d", f.id, last)
		}
		last = id
	}
}

func TestMCPPushResourceSubscriptionsAreLimitedToOwnTasks(t *testing.T) {
	e, lead, worker, wsid, task, st := pushFixture(t)
	other := e.Register("w2", "worker", "claude-code")
	osid := mcpInit(t, e, other)
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	st.event(t, "task_assigned")

	uri := "subspace://task/" + task
	for _, u := range []string{uri, "subspace://thread/task/" + task, "subspace://thread/agent/w1", "subspace://thread/agent/me"} {
		if r := mcpRPC(t, e, worker, wsid, "resources/subscribe", map[string]any{"uri": u}); r["error"] != nil {
			t.Fatalf("subscribe %s: %v", u, r)
		}
	}
	for _, u := range []string{uri, "subspace://thread/task/" + task, "subspace://thread/agent/w1", "subspace://thread/brief/B-1", "subspace://task/T-999", "https://x"} {
		r := mcpRPC(t, e, other, osid, "resources/subscribe", map[string]any{"uri": u})
		if er, _ := r["error"].(map[string]any); er == nil || er["code"] != float64(-32002) {
			t.Fatalf("w2 subscribe %s: %v", u, r)
		}
	}
	if r := mcpRPC(t, e, worker, "", "resources/subscribe", map[string]any{"uri": uri}); r["error"] == nil {
		t.Fatal("subscribe without a session")
	}

	e.Must(worker, "claim_task", map[string]any{"task_id": task})
	if got := st.updatedURIs(t); !got[uri] {
		t.Fatalf("after claim: %v", got)
	}
	e.Must(lead, "post_message", map[string]any{"thread": "task:" + task, "body": "hi"})
	got := st.updatedURIs(t)
	if !got["subspace://thread/task/"+task] || got[uri] {
		t.Fatalf("after message: %v", got)
	}

	for _, u := range []string{uri, "subspace://thread/task/" + task} {
		mcpRPC(t, e, worker, wsid, "resources/unsubscribe", map[string]any{"uri": u})
	}
	e.Must(worker, "update_status", map[string]any{"task_id": task, "status": "in_progress", "progress_pct": 10})
	st.quiet(t)
	e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": "marker"})
	got = st.updatedURIs(t)
	if len(got) != 1 || !got["subspace://thread/agent/w1"] {
		t.Fatalf("after unsubscribe: %v", got)
	}
}

func TestMCPPushResumeReplaysWithinBufferAndResyncsBeyondIt(t *testing.T) {
	t.Setenv("SUBSPACE_MCP_PUSH_BUFFER", "4")
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	worker := e.Register("w1", "worker", "claude-code")
	sid := mcpInit(t, e, worker)
	st := openPush(t, e, worker, sid, "")
	msg := func(n int) {
		e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": fmt.Sprint("m", n)})
	}
	msg(0)
	first := st.next(t, 3*time.Second)
	st.cancel()
	st.ended(t)

	msg(1)
	msg(2)
	st = openPush(t, e, worker, sid, first.id)
	for want := int64(2); want <= 3; want++ {
		f := st.next(t, 3*time.Second)
		if f.id != strconv.FormatInt(want, 10) || f.event != "message" {
			t.Fatalf("replay want id %d got %+v", want, f)
		}
	}
	st.quiet(t)
	st.cancel()
	st.ended(t)

	for i := 3; i < 12; i++ {
		msg(i)
	}
	st = openPush(t, e, worker, sid, "3")
	f := st.next(t, 3*time.Second)
	method, p := f.rpc(t)
	if f.id != "" || method != "notifications/subspace/resync" || p["reason"] != "overflow" || p["dropped"] != float64(5) {
		t.Fatalf("resync frame %+v %v", f, p)
	}
	for want := 9; want <= 12; want++ {
		if f := st.next(t, 3*time.Second); f.id != strconv.Itoa(want) {
			t.Fatalf("after resync want id %d got %+v", want, f)
		}
	}
	st.quiet(t)
	st.cancel()
	st.ended(t)

	st = openPush(t, e, worker, sid, "9999")
	if method, p := st.next(t, 3*time.Second).rpc(t); method != "notifications/subspace/resync" || p["reason"] != "unknown_event_id" {
		t.Fatalf("unknown id: %s %v", method, p)
	}
	st.cancel()
	st.ended(t)

	resp, err := pushGet(e, context.Background(), worker, sid, "abc", "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad Last-Event-ID: %d", resp.StatusCode)
	}
}

func TestMCPPushSecondStreamReplacesTheFirstWith409(t *testing.T) {
	e, lead, worker, wsid, _, first := pushFixture(t)
	second := openPush(t, e, worker, wsid, "")

	f := first.next(t, 3*time.Second)
	if f.event != "close" || !strings.Contains(f.data, `"reason":"replaced"`) || !strings.Contains(f.data, `"status":409`) {
		t.Fatalf("first stream got %+v", f)
	}
	first.ended(t)

	e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": "hi"})
	second.event(t, "message")
}

func TestMCPPushStreamsArePerSession(t *testing.T) {
	e, lead, worker, _, _, a := pushFixture(t)
	b := openPush(t, e, worker, mcpInit(t, e, worker), "")
	e.Must(lead, "post_message", map[string]any{"thread": "agent:w1", "body": "hi"})
	a.event(t, "message")
	b.event(t, "message")
}

func TestMCPPushAuthAndSessionBinding(t *testing.T) {
	e, _, worker, wsid, _, _ := pushFixture(t)
	other := e.Register("w2", "worker", "claude-code")
	ctx := context.Background()
	for _, tc := range []struct {
		name                 string
		tok, sid, lastID, ac string
		want                 int
	}{
		{"no bearer", "", wsid, "", "text/event-stream", 401},
		{"bad bearer", "ssp_nope", wsid, "", "text/event-stream", 401},
		{"owner token", servertest.OwnerToken, wsid, "", "text/event-stream", 403},
		{"accept json", worker, wsid, "", "application/json", 406},
		{"no accept", worker, wsid, "", "", 406},
		{"no session", worker, "", "", "text/event-stream", 400},
		{"unknown session", worker, "deadbeef", "", "text/event-stream", 404},
		{"foreign session", other, wsid, "", "text/event-stream", 404},
		{"bad last id", worker, wsid, "-1", "text/event-stream", 400},
	} {
		resp, err := pushGet(e, ctx, tc.tok, tc.sid, tc.lastID, tc.ac)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
		if tc.want == 401 && resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate", tc.name)
		}
	}
}

func TestMCPPushRevokedAgentCannotOpenStream(t *testing.T) {
	e, _, worker, wsid, _, _ := pushFixture(t)
	if code, out := e.REST("POST", "/api/v1/agents/w1/revoke", map[string]any{}); code != 200 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	resp, err := pushGet(e, context.Background(), worker, wsid, "", "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked: %d", resp.StatusCode)
	}
}

func TestMCPPushDeletingTheSessionEndsTheStream(t *testing.T) {
	e, _, worker, wsid, _, st := pushFixture(t)
	req, _ := http.NewRequest(http.MethodDelete, e.TS.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+worker)
	req.Header.Set("Mcp-Session-Id", wsid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if f := st.next(t, 3*time.Second); f.event != "close" || !strings.Contains(f.data, "session_ended") {
		t.Fatalf("frame %+v", f)
	}
	st.ended(t)
	resp, err = pushGet(e, context.Background(), worker, wsid, "", "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete: %d", resp.StatusCode)
	}
}

func TestMCPPushHeartbeatComments(t *testing.T) {
	t.Setenv("SUBSPACE_MCP_PUSH_PING", "40ms")
	_, _, _, _, _, st := pushFixture(t)
	for range 2 {
		if f := st.next(t, 2*time.Second); f.event != "comment" || f.data != "ping" {
			t.Fatalf("frame %+v", f)
		}
	}
}

func TestMCPPushAdvertisedInInitializeAndDisabledByEnv(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("w1", "worker", "claude-code")
	res := e.RPC(tok, "initialize", map[string]any{})["result"].(map[string]any)
	caps := res["capabilities"].(map[string]any)
	if caps["resources"].(map[string]any)["subscribe"] != true || caps["tools"].(map[string]any)["listChanged"] != true {
		t.Fatalf("capabilities %v", caps)
	}
	if caps["prompts"] == nil {
		t.Fatalf("prompts capability missing: %v", caps)
	}

	t.Setenv("SUBSPACE_MCP_PUSH", "off")
	off := servertest.New(t)
	tok = off.Register("w1", "worker", "claude-code")
	req, _ := http.NewRequest(http.MethodGet, off.TS.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST, DELETE" {
		t.Fatalf("disabled: %d %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}
