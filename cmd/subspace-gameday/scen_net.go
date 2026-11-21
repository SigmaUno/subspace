package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/faultnet"
)

func (o Options) proxy(st *Stack) (*faultnet.Proxy, error) {
	return faultnet.Listen(st.Cfg.Addr, o.Seed, faultnet.Faults{})
}

func networkPartition(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	px, err := r.Opt.proxy(st)
	if err != nil {
		return err
	}
	defer func() { _ = px.Close() }()
	c := st.Client()
	w, err := c.Seed(r.Ctx, r.workers())
	if err != nil {
		return err
	}
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	ac := c.With(px.URL(), time.Second)
	load := StartLoad(r.Ctx, ac, w, 20*time.Millisecond)
	defer load.Stop()
	r.Sleep(300 * time.Millisecond)

	cut := time.Now()
	px.Set(faultnet.Faults{AcceptStall: time.Hour})
	px.ResetAll()
	d, ok := r.Until(r.Opt.Detect, func() (bool, error) { return allTasks(r.Ctx, w, "assigned") })
	if !r.Expect("server expired the leases of the cut-off agents", ok, "all tasks back to assigned") || !r.Detected(d) {
		return nil
	}
	_, seen := load.FirstFailure(cut)
	r.Expect("agents saw the partition", seen, "requests through the proxy failed")
	if code, _, err := c.REST(r.Ctx, http.MethodGet, "/state", nil); err != nil || code != http.StatusOK {
		r.Expect("control plane reachable during the partition", false, "state: %d %v", code, err)
	}

	healed := time.Now()
	px.Set(faultnet.Faults{})
	px.ResetAll()
	for i, a := range w.Workers {
		_, ok := r.Until(r.Opt.Recover, func() (bool, error) {
			_, code, err := ac.Tool(r.Ctx, a.Token, "claim_task", map[string]any{"task_id": w.Tasks[i]})
			return err == nil && code == "", nil
		})
		if !r.Expect("agent "+a.ID+" reclaimed its task", ok, "after the partition healed") {
			return nil
		}
	}
	r.Recovered(time.Since(healed))
	for i, a := range w.Workers {
		t, err := w.Task(r.Ctx, i)
		if err != nil {
			return err
		}
		r.Expect("task "+w.Tasks[i]+" has one owner", str(t, "status") == "in_progress" && str(t, "assignee") == a.ID, "%s by %s", str(t, "status"), str(t, "assignee"))
	}
	load.Stop()
	acked := load.Acked()
	missing, err := missingAcked(r.Ctx, c, acked)
	if err != nil {
		return err
	}
	r.Expect("no acknowledged write lost", missing == 0, "%d of %d missing", missing, len(acked))
	ok, detail, err := c.AuditOK(r.Ctx)
	if err != nil {
		return err
	}
	r.Expect("audit chain verifies", ok, "%s", detail)
	return nil
}

func networkLatency(r *Run) error {
	const lat = 60 * time.Millisecond
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	px, err := r.Opt.proxy(st)
	if err != nil {
		return err
	}
	defer func() { _ = px.Close() }()
	c := st.Client()
	w, err := c.Seed(r.Ctx, r.workers())
	if err != nil {
		return err
	}
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	ac := c.With(px.URL(), 5*time.Second)
	load := StartLoad(r.Ctx, ac, w, 20*time.Millisecond)
	defer load.Stop()
	r.Sleep(300 * time.Millisecond)

	slowed := time.Now()
	px.Set(faultnet.Faults{Latency: lat, Jitter: lat / 4})
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) { _, ok := load.FirstSlow(slowed, lat); return ok, nil })
	if !r.Expect("latency visible to agents", ok, "a call took over %s", lat) {
		return nil
	}
	if first, _ := load.FirstSlow(slowed, lat); !r.Detected(first.Sub(slowed)) {
		return nil
	}
	r.Sleep(1500 * time.Millisecond)

	cleared := time.Now()
	px.Set(faultnet.Faults{})
	_, ok = r.Until(r.Opt.Recover, func() (bool, error) { _, ok := load.FirstFast(cleared, lat); return ok, nil })
	if r.Expect("calls fast again", ok, "after the latency was removed") {
		end, _ := load.FirstFast(cleared, lat)
		r.Recovered(end.Sub(cleared))
	}
	load.Stop()
	r.Expect("no request failed under latency", load.Failures() == 0, "%d failed, slowest %s", load.Failures(), load.Max().Round(time.Millisecond))
	held, err := allTasks(r.Ctx, w, "in_progress")
	if err != nil {
		return err
	}
	r.Expect("leases held under latency", held, "all tasks still in_progress")
	acked := load.Acked()
	missing, err := missingAcked(r.Ctx, c, acked)
	if err != nil {
		return err
	}
	r.Expect("no acknowledged write lost", missing == 0, "%d of %d missing", missing, len(acked))
	return nil
}

func sseStorm(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, 1)
	if err != nil {
		return err
	}
	defer w.Cleanup()

	subs := max(r.Opt.Subs, 10)
	slow := subs / 10
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(r.Ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	var all []*Subscriber
	for i := range subs {
		all = append(all, Subscribe(ctx, st.URL, st.Token, i < slow, &wg))
	}
	ready := func() (bool, error) {
		for _, s := range all {
			select {
			case <-s.hello:
			default:
				return false, nil
			}
		}
		return true, nil
	}
	if _, ok := r.Until(r.Opt.Detect, ready); !r.Expect("subscribers connected", ok, "%d streams", subs) {
		return nil
	}

	var probeMax atomic.Int64
	var probeFail atomic.Int64
	probeDone := make(chan struct{})
	stopProbe := make(chan struct{})
	pc := c.With(st.URL, 5*time.Second)
	go func() {
		defer close(probeDone)
		for {
			t0 := time.Now()
			code, _, err := pc.REST(r.Ctx, http.MethodGet, "/state", nil)
			if err != nil || code != http.StatusOK {
				probeFail.Add(1)
			}
			d := int64(time.Since(t0))
			for {
				m := probeMax.Load()
				if d <= m || probeMax.CompareAndSwap(m, d) {
					break
				}
			}
			select {
			case <-stopProbe:
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()

	burst := max(r.Opt.Burst, 50)
	var next atomic.Int64
	var posters sync.WaitGroup
	var postFail atomic.Int64
	for range 4 {
		posters.Add(1)
		go func() {
			defer posters.Done()
			for {
				n := next.Add(1)
				if n > int64(burst) {
					return
				}
				_, code, err := c.Tool(r.Ctx, w.Leader.Token, "post_message", map[string]any{"thread": "brief:" + w.BriefID, "body": fmt.Sprintf("storm %d %s", n, randHex(24))})
				if err != nil || code != "" {
					postFail.Add(1)
				}
			}
		}()
	}
	posters.Wait()
	fired := time.Now()

	for _, s := range all[:slow] {
		s.Release()
	}
	want := int64(burst - int(postFail.Load()))
	complete := func() (bool, error) {
		for _, s := range all {
			if !s.Complete(want) {
				return false, nil
			}
		}
		return true, nil
	}
	_, ok := r.Until(r.Opt.Recover, complete)
	close(stopProbe)
	<-probeDone

	var short, lagged, resynced, dropped int
	for _, s := range all {
		if !s.Complete(want) {
			short++
		}
		if s.lagged.Load() > 0 {
			lagged++
		}
		if s.resync.Load() > 0 {
			resynced++
		}
		if s.closed.Load() {
			dropped++
		}
	}
	if ok {
		r.Recovered(time.Since(fired))
	}
	r.Expect("every subscriber got all events or a lagged/resync marker", ok && short == 0, "%d of %d incomplete (%d lagged, %d resync)", short, subs, lagged, resynced)
	r.Expect("event streams survived", dropped == 0, "%d of %d streams ended", dropped, subs)
	r.Expect("posts accepted during the storm", postFail.Load() == 0, "%d of %d failed", postFail.Load(), burst)
	limit := r.Opt.Detect / 2
	r.Expect("server stayed responsive", probeFail.Load() == 0 && time.Duration(probeMax.Load()) <= limit, "%d probe failures, slowest %s, limit %s", probeFail.Load(), time.Duration(probeMax.Load()).Round(time.Millisecond), limit)
	return nil
}
