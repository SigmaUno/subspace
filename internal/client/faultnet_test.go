package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/faultnet"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func (e *env) proxy(seed uint64, f faultnet.Faults) *faultnet.Proxy {
	e.t.Helper()
	p, err := faultnet.Listen(strings.TrimPrefix(e.url, "http://"), seed, f)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = p.Close() })
	return p
}

func flakyClient(url, token string) *client.MCP {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return client.NewMCP(url, token,
		client.WithHTTPClient(&http.Client{Transport: tr, Timeout: time.Second}),
		client.WithRetries(40),
		client.WithSleep(func(ctx context.Context, _ time.Duration) error {
			select {
			case <-time.After(5 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}))
}

func TestFaultyNetworkMutationsApplyExactlyOnce(t *testing.T) {
	e := newEnv(t)
	tm := e.team()
	b := e.brief()
	const agents, rounds = 4, 12
	ids := make([]string, agents)
	tokens := make([]string, agents)
	for i := range agents {
		id := fmt.Sprintf("flaky-%d", i)
		reg, err := e.owner.RegisterAgent(e.ctx, board.AgentInput{ID: id, Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		task := e.readyTask(tm.leader, b.ID, id)
		if _, err := tm.leader.Assign(e.ctx, task.ID, id, ""); err != nil {
			t.Fatal(err)
		}
		ids[i], tokens[i] = task.ID, reg.Connect.Token
	}

	p := e.proxy(7, faultnet.Faults{
		Latency: time.Millisecond, Jitter: 2 * time.Millisecond,
		ResetUp: 0.1, ResetDown: 0.3, Truncate: 0.1, HalfOpen: 0.02,
	})
	var wg sync.WaitGroup
	errs := make(chan error, agents)
	for i := range agents {
		wg.Go(func() {
			c := flakyClient(p.URL()+"/mcp", tokens[i])
			if err := c.Initialize(e.ctx); err != nil {
				errs <- fmt.Errorf("agent %d initialize: %w", i, err)
				return
			}
			if _, err := c.ClaimTask(e.ctx, ids[i]); err != nil {
				errs <- fmt.Errorf("agent %d claim: %w", i, err)
				return
			}
			for r := range rounds {
				if _, err := c.AddNote(e.ctx, ids[i], fmt.Sprintf("note %d/%d", i, r)); err != nil {
					errs <- fmt.Errorf("agent %d note %d: %w", i, r, err)
					return
				}
				pct := r * 100 / rounds
				if _, err := c.UpdateStatus(e.ctx, board.StatusInput{TaskID: ids[i], Status: model.StatusInProgress, ProgressPct: &pct, Note: fmt.Sprintf("progress %d/%d", i, r)}); err != nil {
					errs <- fmt.Errorf("agent %d status %d: %w", i, r, err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	st := p.Stats()
	if st.Resets == 0 || st.Truncated == 0 || st.HalfOpen == 0 {
		t.Fatalf("faults not exercised: %+v", st)
	}

	for i, id := range ids {
		task, err := e.owner.GetTask(e.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != model.StatusInProgress {
			t.Errorf("%s status %s", id, task.Status)
		}
		events, err := e.owner.Audit(e.ctx, store.AuditFilter{TaskID: id, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		count := map[string]int{}
		for _, ev := range events {
			count[ev.Tool]++
			if ev.Tool == "update_status" && ev.Note != nil {
				count[*ev.Note]++
			}
		}
		if count["claim_task"] != 1 {
			t.Errorf("%s claimed %d times in audit", id, count["claim_task"])
		}
		if count["update_status"] != rounds {
			t.Errorf("%s update_status audited %d times, want %d", id, count["update_status"], rounds)
		}
		msgs, err := e.owner.ThreadMessages(e.ctx, "task", id, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			count[m.Body]++
		}
		for r := range rounds {
			if n := count[fmt.Sprintf("progress %d/%d", i, r)]; n != 1 {
				t.Errorf("%s progress %d audited %d times", id, r, n)
			}
			if n := count[fmt.Sprintf("note %d/%d", i, r)]; n != 1 {
				t.Errorf("%s note %d stored %d times", id, r, n)
			}
		}
	}
}

func TestFaultyNetworkSessionRecovery(t *testing.T) {
	e := newEnv(t)
	tm := e.team()
	b := e.brief()
	task := e.readyTask(tm.leader, b.ID, "session")
	if _, err := tm.leader.Assign(e.ctx, task.ID, "wally", ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.owner.RotateToken(e.ctx, "wally")
	if err != nil {
		t.Fatal(err)
	}
	p := e.proxy(11, faultnet.Faults{ResetDown: 0.25, Truncate: 0.1})
	c := flakyClient(p.URL()+"/mcp", r.Connect.Token)
	if err := c.Initialize(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimTask(e.ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{c.SessionID(): true}
	for round := range 6 {
		p.ResetAll()
		for range 8 {
			direct := client.NewMCP(e.url+"/mcp", r.Connect.Token)
			if err := direct.Initialize(e.ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.AddNote(e.ctx, task.ID, fmt.Sprintf("after eviction %d", round)); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if err := c.Ping(e.ctx); err != nil {
			t.Fatalf("round %d ping: %v", round, err)
		}
		if seen[c.SessionID()] {
			t.Fatalf("round %d: session %q not renewed after eviction", round, c.SessionID())
		}
		seen[c.SessionID()] = true
	}
	msgs, err := e.owner.ThreadMessages(e.ctx, "task", task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, m := range msgs {
		count[m.Body]++
	}
	for round := range 6 {
		if n := count[fmt.Sprintf("after eviction %d", round)]; n != 1 {
			t.Fatalf("note %d stored %d times", round, n)
		}
	}
}

func TestFaultyNetworkEventResumeIsGapless(t *testing.T) {
	e := newEnv(t)
	if _, err := e.owner.RegisterAgent(e.ctx, board.AgentInput{ID: "wally", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	p := e.proxy(23, faultnet.Faults{Latency: time.Millisecond, Jitter: time.Millisecond, ResetDown: 0.08, Truncate: 0.08, Bandwidth: 256 << 10})
	sub := client.NewOwner(p.URL(), ownerToken)
	const total = 300
	topic := "thread:agent:wally"

	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()
	hello, err := e.owner.EventsFrom(ctx, "", topic)
	if err != nil {
		t.Fatal(err)
	}
	first, err := hello.Next()
	if err != nil || first.Name != "hello" {
		t.Fatalf("hello: %+v %v", first, err)
	}
	_ = hello.Close()
	go func() {
		for i := range total {
			if _, err := e.owner.PostThreadMessage(ctx, "agent", "wally", fmt.Sprintf("m-%d", i)); err != nil {
				t.Errorf("post %d: %v", i, err)
				return
			}
			if i%25 == 0 {
				p.ResetAll()
			}
		}
	}()

	got := map[string]int{}
	var order []string
	var lastID int64
	last, reconnects := first.ID, 0
	for len(got) < total && ctx.Err() == nil {
		stream, err := sub.EventsFrom(ctx, last, topic)
		if err != nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		reconnects++
		for len(got) < total {
			ev, err := stream.Next()
			if err != nil {
				break
			}
			if ev.ID != "" {
				var id int64
				if _, err := fmt.Sscan(ev.ID, &id); err != nil || id < lastID {
					t.Fatalf("event id %q after %d", ev.ID, lastID)
				}
				lastID, last = id, ev.ID
			}
			switch ev.Name {
			case "lagged", "resync":
				t.Fatalf("stream lost events: %s %s", ev.Name, ev.Data)
			case "message.created":
				env, err := ev.Envelope()
				if err != nil {
					t.Fatal(err)
				}
				var m model.Message
				if err := json.Unmarshal(env.Data, &m); err != nil {
					t.Fatal(err)
				}
				got[m.Body]++
				order = append(order, m.Body)
			}
		}
		_ = stream.Close()
	}
	if len(got) != total {
		var miss []int
		for i := range total {
			if got[fmt.Sprintf("m-%d", i)] == 0 {
				miss = append(miss, i)
			}
		}
		t.Fatalf("received %d of %d messages after %d connections, missing %v, last %s, %+v", len(got), total, reconnects, miss, last, p.Stats())
	}
	for i, body := range order {
		if got[body] != 1 {
			t.Fatalf("%s delivered %d times", body, got[body])
		}
		if want := fmt.Sprintf("m-%d", i); body != want {
			t.Fatalf("event %d is %s, want %s", i, body, want)
		}
	}
	if reconnects < 5 {
		t.Fatalf("only %d connections, faults not exercised", reconnects)
	}
}

func TestFaultyNetworkOwnerReadsRetryWritesDoNot(t *testing.T) {
	e := newEnv(t)
	if _, err := e.owner.RegisterAgent(e.ctx, board.AgentInput{ID: "wally", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	p := e.proxy(29, faultnet.Faults{ResetDown: 0.3, Truncate: 0.1})
	o := client.NewOwner(p.URL(), ownerToken, client.WithRetries(40),
		client.WithHTTPClient(&http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 2 * time.Second}),
		client.WithSleep(func(context.Context, time.Duration) error { return nil }))
	failed := 0
	for i := range 40 {
		if a, err := o.GetAgent(e.ctx, "wally"); err != nil || a.ID != "wally" {
			t.Fatalf("read %d: %v", i, err)
		}
		if _, err := o.PostThreadMessage(e.ctx, "agent", "wally", fmt.Sprintf("w-%d", i)); err != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("no write hit a fault")
	}
	msgs, err := e.owner.ThreadMessages(e.ctx, "agent", "wally", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.Body] {
			t.Fatalf("%s stored twice", m.Body)
		}
		seen[m.Body] = true
	}
	if len(msgs) < 40-failed {
		t.Fatalf("%d messages stored, %d writes succeeded", len(msgs), 40-failed)
	}
}
