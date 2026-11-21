package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type live struct {
	*env
	db      string
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	entered atomic.Int32
	base    string
	tok     string
	taskID  string
	claim   time.Time
}

func startLive(t *testing.T) *live {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	db := filepath.Join(t.TempDir(), "t.db")
	s, err := New(ctx, Config{DBPath: db, OwnerToken: ownerTok, PublicURL: "http://sub.test", Lease: time.Minute,
		FlushEvery: time.Hour, SweepEvery: time.Hour, BackupInterval: 0, ShutdownTimeout: 10 * time.Second,
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	l := &live{db: db, cancel: cancel, done: make(chan struct{})}
	inner := s.handler
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.entered.Add(1)
		inner.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		l.err = s.Serve(ctx, ln)
		close(l.done)
	}()
	l.base = "http://" + ln.Addr().String()
	l.env = &env{t: t, ts: &httptest.Server{URL: l.base}, srv: s}
	t.Cleanup(func() {
		cancel()
		<-l.done
		_ = s.Close()
	})

	lead := l.register("rex", "leader", "other")
	l.tok = l.register("w1", "worker", "claude-code")
	bid := l.brief(map[string]any{})
	l.taskID = l.approvedPlan(lead, bid, planTask("a", nil))[0]
	l.must(lead, "assign", map[string]any{"task_id": l.taskID, "agent_id": "w1"})
	l.must(l.tok, "claim_task", map[string]any{"task_id": l.taskID})
	l.claim = l.dbTask(t).LeaseExpiresAt.UTC()
	time.Sleep(20 * time.Millisecond)
	return l
}

func (l *live) dbTask(t *testing.T) *model.Task {
	t.Helper()
	var tk *model.Task
	err := l.srv.Store.View(context.Background(), func(tx store.Tx) (e error) { tk, e = tx.GetTask(l.taskID); return })
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func (l *live) shutdown(t *testing.T) {
	t.Helper()
	l.cancel()
	select {
	case <-l.done:
		if l.err != nil {
			t.Fatalf("serve: %v", l.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
	if err := l.srv.Close(); err != nil {
		t.Fatal(err)
	}
}

func reopen(t *testing.T, db string) store.Store {
	t.Helper()
	st, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type held struct {
	pw   *io.PipeWriter
	rest string
	res  chan heldResult
}

type heldResult struct {
	code int
	body map[string]any
	err  error
}

func (l *live) hold(t *testing.T, path, bearer, first, rest string) *held {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", l.base+path, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	h := &held{pw: pw, rest: rest, res: make(chan heldResult, 1)}
	go func() {
		resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
		if err != nil {
			h.res <- heldResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		h.res <- heldResult{code: resp.StatusCode, body: out}
	}()
	if _, err := io.WriteString(pw, first); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *held) release(t *testing.T) heldResult {
	t.Helper()
	if _, err := io.WriteString(h.pw, h.rest); err != nil {
		t.Fatal(err)
	}
	_ = h.pw.Close()
	select {
	case r := <-h.res:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("held request never completed")
		return heldResult{}
	}
}

func waitEntered(t *testing.T, l *live, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.entered.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d requests entered, want %d", l.entered.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShutdownDrainsInFlightRequestsAndClosesStreams(t *testing.T) {
	l := startLive(t)
	before := l.entered.Load()

	req, _ := http.NewRequest("GET", l.base+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	sse, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sse.Body.Close() }()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(sse.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	rest := l.hold(t, "/api/v1/briefs", ownerTok, `{"title":"held",`, `"body":"b","repos":[],"max_cost_eur":1,"max_parallel_agents":1}`)
	mcp := l.hold(t, "/mcp", l.tok, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"heartbeat",`,
		`"arguments":{"task_id":"`+l.taskID+`"}}}`)
	waitEntered(t, l, before+3)

	l.cancel()

	closed := false
	timeout := time.After(3 * time.Second)
	for !closed {
		select {
		case ln, ok := <-lines:
			if !ok {
				t.Fatal("stream ended without a close event")
			}
			closed = strings.HasPrefix(ln, "event: close")
		case <-timeout:
			t.Fatal("no close event after cancel")
		}
	}
	for open := true; open; {
		select {
		case _, open = <-lines:
		case <-time.After(3 * time.Second):
			t.Fatal("stream still open after close event")
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(l.base, "http://"), 200*time.Millisecond)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-l.done:
		t.Fatalf("server stopped with requests in flight: %v", l.err)
	case <-time.After(200 * time.Millisecond):
	}

	if r := rest.release(t); r.err != nil || r.code != 201 || r.body["title"] != "held" {
		t.Fatalf("in-flight REST call: %+v", r)
	}
	r := mcp.release(t)
	if r.err != nil {
		t.Fatalf("in-flight MCP call: %v", r.err)
	}
	if res, _ := r.body["result"].(map[string]any); res == nil || res["isError"] == true {
		t.Fatalf("in-flight MCP call: %+v", r)
	}

	select {
	case <-l.done:
		if l.err != nil {
			t.Fatalf("serve: %v", l.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after drain")
	}
	if err := l.srv.Close(); err != nil {
		t.Fatal(err)
	}

	st := reopen(t, l.db)
	var briefs []*model.Brief
	var tk *model.Task
	var agent *model.Agent
	err = st.View(context.Background(), func(tx store.Tx) (e error) {
		if briefs, e = tx.ListBriefs(store.BriefFilter{}); e != nil {
			return
		}
		if tk, e = tx.GetTask(l.taskID); e != nil {
			return
		}
		agent, e = tx.GetAgent("w1")
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, b := range briefs {
		titles = append(titles, b.Title)
	}
	if !strings.Contains(strings.Join(titles, ","), "held") {
		t.Fatalf("held brief missing after drain: %v", titles)
	}
	if tk.Status != model.StatusInProgress || tk.Assignee == nil || *tk.Assignee != "w1" {
		t.Fatalf("lease lost on shutdown: %s assignee=%v", tk.Status, tk.Assignee)
	}
	if tk.LeaseExpiresAt == nil || !tk.LeaseExpiresAt.After(l.claim) {
		t.Fatalf("in-flight heartbeat not persisted: lease %v, claim lease %v", tk.LeaseExpiresAt, l.claim)
	}
	if agent.LastSeenAt == nil {
		t.Fatal("agent last_seen not persisted")
	}
	integrityOK(t, l.db)
}

func TestHeartbeatsBeforeShutdownArePersisted(t *testing.T) {
	l := startLive(t)
	for range 5 {
		out := l.must(l.tok, "heartbeat", map[string]any{"task_id": l.taskID})
		if out["ok"] != true {
			t.Fatalf("heartbeat: %v", out)
		}
	}
	if got := l.dbTask(t).LeaseExpiresAt; got == nil || !got.UTC().Equal(l.claim) {
		t.Fatalf("heartbeat reached the DB before the flush interval: %v vs %v", got, l.claim)
	}
	overlay := l.task(l.taskID)["lease_expires_at"].(string)

	l.shutdown(t)

	st := reopen(t, l.db)
	var tk *model.Task
	var agent *model.Agent
	err := st.View(context.Background(), func(tx store.Tx) (e error) {
		if tk, e = tx.GetTask(l.taskID); e != nil {
			return
		}
		agent, e = tx.GetAgent("w1")
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != model.StatusInProgress {
		t.Fatalf("task status after shutdown: %s", tk.Status)
	}
	if tk.LastHeartbeatAt == nil || tk.LeaseExpiresAt == nil || !tk.LeaseExpiresAt.After(l.claim) {
		t.Fatalf("heartbeats lost on shutdown: hb=%v lease=%v claim=%v", tk.LastHeartbeatAt, tk.LeaseExpiresAt, l.claim)
	}
	seen, err := time.Parse(time.RFC3339Nano, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if d := tk.LeaseExpiresAt.Sub(seen).Abs(); d > time.Millisecond {
		t.Fatalf("persisted lease %v differs from the lease agents saw %v", tk.LeaseExpiresAt, seen)
	}
	if agent.LastSeenAt == nil {
		t.Fatal("agent last_seen lost on shutdown")
	}
	integrityOK(t, l.db)
}
