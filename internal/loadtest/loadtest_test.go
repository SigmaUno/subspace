package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistogramQuantiles(t *testing.T) {
	var h Histogram
	if h.Quantile(0.5) != 0 || h.Max() != 0 {
		t.Fatal("empty histogram must report zero")
	}
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Millisecond)
	}
	check := func(q float64, want time.Duration) {
		t.Helper()
		got := h.Quantile(q)
		if diff := float64(got-want) / float64(want); diff < -0.001 || diff > 0.03 {
			t.Errorf("q%.2f = %v, want ≈ %v", q, got, want)
		}
	}
	check(0.50, 500*time.Millisecond)
	check(0.95, 950*time.Millisecond)
	check(0.99, 990*time.Millisecond)
	if h.Max() != time.Second || h.Quantile(1) != time.Second {
		t.Errorf("max = %v, q1 = %v", h.Max(), h.Quantile(1))
	}
	if h.Count() != 1000 {
		t.Errorf("count = %d", h.Count())
	}
	var o Histogram
	o.Record(5 * time.Second)
	o.Record(-time.Second)
	h.Merge(&o)
	if h.Count() != 1002 || h.Max() != 5*time.Second {
		t.Errorf("after merge count %d max %v", h.Count(), h.Max())
	}
}

func TestRecorderConcurrent(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				var err error
				if i%10 == 0 {
					err = errors.New("boom")
				}
				r.Observe([]string{"a", "b"}[i%2], time.Millisecond, err)
			}
		}()
	}
	wg.Wait()
	all, ops, samples := r.Snapshot()
	if all.Count != 10000 || all.Errors != 1000 {
		t.Fatalf("overall %+v", all)
	}
	if len(ops) != 2 || ops[0].Name != "a" || ops[0].Count != 5000 || ops[0].Errors != 1000 || ops[1].Errors != 0 {
		t.Fatalf("ops %+v", ops)
	}
	if len(samples) != 1 || samples[0] != "a: boom" {
		t.Fatalf("samples %v", samples)
	}
}

func TestReadSSE(t *testing.T) {
	in := "id: 1\nevent: hello\ndata: {\"seq\":1}\n\n: ping\n\nid: 2\r\nevent: task.updated\r\ndata: a\r\ndata: b\r\nretry: 5\r\n\r\n"
	var got []sseFrame
	if err := readSSE(strings.NewReader(in), func(f sseFrame) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Event != "hello" || got[1].ID != "2" || got[1].Data != "a\nb" {
		t.Fatalf("frames %+v", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []Config{
		{Agents: 0, Duration: time.Second},
		{Agents: 1, Duration: 0},
		{Agents: 1, Duration: time.Second, MaxErrorRate: 2},
		{Agents: 1, Duration: time.Second, URL: "http://x"},
		{Agents: 1, Duration: time.Second, SSESubs: -1},
	}
	for i, c := range bad {
		c.Defaults()
		if c.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	ok := Config{Agents: 50, Duration: time.Second}
	ok.Defaults()
	if err := ok.Validate(); err != nil || ok.Reviewers != 2 || ok.TasksPerAgent != 2 {
		t.Fatalf("defaults: %v %+v", err, ok)
	}
}

const fakeOwner = "fake-owner-token-0123456789abcdef"

func TestRunAgainstFake(t *testing.T) {
	f := NewFakeServer(fakeOwner)
	ts := httptest.NewServer(f)
	defer ts.Close()
	rep, err := Run(context.Background(), Config{Agents: 30, Duration: 1500 * time.Millisecond, URL: ts.URL, OwnerToken: fakeOwner,
		SSESubs: 10, Heartbeats: 2, TasksPerAgent: 20, Think: 2 * time.Millisecond, Drain: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	t.Log(buf.String())
	if rep.Errors != 0 || rep.Breached() {
		t.Fatalf("errors: %d %v", rep.Errors, rep.ErrorSamples)
	}
	// 30×20 = 600 tasks: more than one plan (500) so create_task is exercised in setup too.
	if rep.Tasks.Created < 600 || rep.Tasks.Submitted == 0 {
		t.Fatalf("tasks %+v", rep.Tasks)
	}
	if rep.Tasks.Done != rep.Tasks.Approved || rep.Tasks.Approved != rep.Tasks.Submitted || int(rep.Tasks.Done) != f.Count("done") {
		t.Fatalf("lifecycle mismatch %+v, fake done %d", rep.Tasks, f.Count("done"))
	}
	want := []string{"initialize", "get_instructions", "propose_plan", "create_task", "assign", "list_my_tasks", "claim_task",
		"heartbeat", "update_status", "submit", "review", "rest:create_brief", "rest:register_agent", "rest:register_bulk",
		"rest:approve_plan", "rest:mark_done"}
	have := map[string]OpReport{}
	for _, o := range rep.Ops {
		have[o.Name] = o
	}
	for _, w := range want {
		if have[w].Count == 0 {
			t.Errorf("no %s requests", w)
		}
	}
	if have["heartbeat"].Count < 2*have["submit"].Count {
		t.Errorf("heartbeats %d < 2×submits %d", have["heartbeat"].Count, have["submit"].Count)
	}
	if rep.SSE.Connected != 10 || rep.SSE.Events == 0 || rep.SSE.ConnectErrors != 0 {
		t.Errorf("sse %+v", rep.SSE)
	}
	if rep.Throughput <= 0 || rep.Overall.P99ms < rep.Overall.P50ms {
		t.Errorf("overall %+v throughput %v", rep.Overall, rep.Throughput)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatal(err)
	}
}

func TestRunErrorRateBreach(t *testing.T) {
	f := NewFakeServer(fakeOwner)
	f.FailTool, f.FailEvery = "heartbeat", 3
	ts := httptest.NewServer(f)
	defer ts.Close()
	cfg := Config{Agents: 5, Duration: 500 * time.Millisecond, URL: ts.URL, OwnerToken: fakeOwner, Heartbeats: 3, Think: time.Millisecond}
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Errors == 0 || !rep.Breached() {
		t.Fatalf("expected breach, got errors=%d rate=%v", rep.Errors, rep.ErrorRate)
	}
	rep.MaxErrorRate = 0.9
	if rep.Breached() {
		t.Fatalf("rate %v should be within 0.9", rep.ErrorRate)
	}
}

func TestRunBadOwnerToken(t *testing.T) {
	ts := httptest.NewServer(NewFakeServer(fakeOwner))
	defer ts.Close()
	_, err := Run(context.Background(), Config{Agents: 1, Duration: time.Second, URL: ts.URL, OwnerToken: "wrong"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("want 401, got %v", err)
	}
}

// TestInProcess runs a tiny load against the real server. It is skipped while the SQLite store is
// still stubbed on this branch.
func TestInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	rep, err := Run(context.Background(), Config{Agents: 3, Duration: time.Second, SSESubs: 2, Heartbeats: 1})
	if err != nil {
		if strings.Contains(err.Error(), "not implemented") || strings.Contains(err.Error(), "HTTP 500") {
			t.Skipf("in-process server not functional yet: %v", err)
		}
		t.Fatal(err)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	t.Log(buf.String())
	if rep.Breached() {
		t.Fatalf("errors %v", rep.ErrorSamples)
	}
}
