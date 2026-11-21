package acceptance

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	haNodes      = 2
	haFailover   = 5 * time.Second
	haPushWait   = 20 * time.Second
	haWatchAgent = "worker-1"
	haKillAfter  = 3
)

type haState struct {
	cl         *servertest.Cluster
	sse        []*sseWatch
	push       []*pushWatch
	killed     int
	survivor   int
	killedAt   time.Time
	notesAtCut int
	sseAtCut   []int64
	failover   time.Duration
	result     chan error
}

func (fl *fleet) startCluster(t *testing.T, cfg server.Config) {
	t.Helper()
	cl := servertest.StartCluster(t, haNodes, cfg)
	fl.ha = &haState{cl: cl, killed: -1, survivor: -1, result: make(chan error, 1)}
	fl.url, fl.stop = cl.Proxy.URL, cl.Stop
}

func (fl *fleet) startWatchers(ctx context.Context, g *group) error {
	h := fl.ha
	for i := range haNodes {
		w := &sseWatch{owner: client.NewOwner(fl.url, servertest.OwnerToken, client.WithHTTPClient(pinnedClient(strconv.Itoa(i)))),
			topics: []string{"board", "agents"}}
		h.sse = append(h.sse, w)
		g.run(ctx, fmt.Sprintf("sse-%d", i), func(ctx context.Context) error {
			w.run(ctx, func() {})
			return nil
		})
		p, err := newPushWatch(ctx, fl.url, fl.tokens[haWatchAgent], strconv.Itoa(i), "subspace://thread/agent/me")
		if err != nil {
			return fmt.Errorf("push watcher %d: %w", i, err)
		}
		h.push = append(h.push, p)
	}
	for i := range haNodes {
		if err := fl.bus.until(ctx, func() bool { return h.sse[i].connects.Load() > 0 }); err != nil {
			return err
		}
	}
	g.run(ctx, "failover", func(ctx context.Context) error {
		h.result <- fl.failOver(ctx)
		return nil
	})
	return nil
}

func (h *haState) wait(ctx context.Context) error {
	select {
	case err := <-h.result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("waiting for the failover to finish: %w", ctx.Err())
	}
}

func (fl *fleet) pushAll(ctx context.Context, body string, strict bool) error {
	h := fl.ha
	updates := make([]int, len(h.push))
	for i, p := range h.push {
		updates[i] = p.updated()
	}
	m, err := fl.owner.PostThreadMessage(ctx, "agent", haWatchAgent, body)
	if err != nil {
		return fmt.Errorf("post %q: %w", body, err)
	}
	deadline := time.Now().Add(haPushWait)
	for i, p := range h.push {
		for !p.has(m.ID) {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return fmt.Errorf("push watcher %d never saw message %d (%s)", i, m.ID, body)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for strict && p.updated() <= updates[i] {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return fmt.Errorf("push watcher %d got no resource update for message %d (%s)", i, m.ID, body)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return nil
}

func (fl *fleet) failOver(ctx context.Context) error {
	h := fl.ha
	leader, err := h.cl.Leader(ctx)
	if err != nil {
		return err
	}
	if err := fl.bus.until(ctx, func() bool {
		v := fl.world.view()
		done := 0
		for _, t := range v.tasks {
			if t.Status == model.StatusDone {
				done++
			}
		}
		busy := false
		for _, c := range v.candidates {
			if c.State != mergequeue.StateMerged {
				busy = true
			}
		}
		return done >= haKillAfter || (done >= 1 && busy)
	}); err != nil {
		return err
	}
	if err := fl.pushAll(ctx, "before the failover", true); err != nil {
		return err
	}
	for _, w := range h.sse {
		h.sseAtCut = append(h.sseAtCut, w.events.Load())
	}
	h.notesAtCut = len(fl.sink.all())
	h.killed, h.killedAt = leader, time.Now()
	h.cl.Kill(leader)
	next, err := h.cl.Leader(ctx)
	if err != nil {
		return err
	}
	h.failover = time.Since(h.killedAt)
	h.survivor = next
	if next == leader {
		return fmt.Errorf("killed node %d is still the leader", leader)
	}
	if err := fl.pushAll(ctx, "after the failover", false); err != nil {
		return err
	}
	deadline := time.Now().Add(haPushWait)
	for h.push[leader].resynced() == 0 {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("push watcher of the killed node never reconnected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fl.pushAll(ctx, "after the reconnect", true)
}

func (fl *fleet) verifyHA(ctx context.Context, t *testing.T) {
	t.Helper()
	h := fl.ha
	if h.killed < 0 || h.survivor < 0 {
		t.Fatalf("the leader was never killed: %+v", h)
	}
	if h.failover > haFailover {
		t.Errorf("failover took %s, limit %s", h.failover, haFailover)
	}
	st, err := h.cl.Status(ctx, h.survivor)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Leader || st.Backend != "postgres" || st.LeaderInstance == nil || *st.LeaderInstance != st.Instance {
		t.Errorf("survivor ha status %+v", st)
	}
	if alive := h.cl.Alive(); len(alive) != 1 || alive[0] != h.survivor {
		t.Errorf("alive nodes %v, want only %d", alive, h.survivor)
	}
	for i, w := range h.sse {
		if w.events.Load() <= h.sseAtCut[i] {
			t.Errorf("sse watcher on node %d got no event after the failover (%d at the cut)", i, h.sseAtCut[i])
		}
	}
	if c := h.sse[h.killed].connects.Load(); c < 2 {
		t.Errorf("sse watcher of the killed node connected %d times, want a reconnect", c)
	}
	if c := h.sse[h.survivor].connects.Load(); c != 1 {
		t.Errorf("sse watcher of the surviving node connected %d times, want 1", c)
	}
	v := fl.world.view()
	var before, after int
	for _, c := range v.candidates {
		if c.State == mergequeue.StateMerged {
			if c.UpdatedAt.After(h.killedAt) {
				after++
			} else {
				before++
			}
		}
	}
	if before == 0 || after == 0 {
		t.Errorf("merge queue merged %d candidates before and %d after the failover, want both", before, after)
	}
	if n := len(fl.sink.all()); n <= h.notesAtCut {
		t.Errorf("no notification was sent after the failover (%d at the cut, %d now)", h.notesAtCut, n)
	}
}
