package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type receiver struct {
	srv    *http.Server
	URL    string
	status atomic.Int32
	mu     sync.Mutex
	ids    map[string]int
}

func newReceiver(status int) (*receiver, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	rc := &receiver{URL: "http://" + ln.Addr().String() + "/hook", ids: map[string]int{}}
	rc.status.Store(int32(status)) //nolint:gosec // HTTP status code
	rc.srv = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rc.mu.Lock()
		rc.ids[req.Header.Get("X-Subspace-Delivery")]++
		rc.mu.Unlock()
		w.WriteHeader(int(rc.status.Load()))
	})}
	go func() { _ = rc.srv.Serve(ln) }()
	return rc, nil
}

func (rc *receiver) hits() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	n := 0
	for _, v := range rc.ids {
		n += v
	}
	return n
}

func (rc *receiver) attempts(id string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.ids[id]
}

func (rc *receiver) Close() { _ = rc.srv.Close() }

func setenv(k, v string) func() {
	old, had := os.LookupEnv(k)
	_ = os.Setenv(k, v)
	return func() {
		if had {
			_ = os.Setenv(k, old)
		} else {
			_ = os.Unsetenv(k)
		}
	}
}

func deliveries(r *Run, c *Client, hook string) ([]map[string]any, error) {
	out, err := c.Must(r.Ctx, http.MethodGet, "/webhooks-out/"+hook+"/deliveries?limit=1000", nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return list(out, "deliveries"), nil
}

func countState(ds []map[string]any, state string) int {
	n := 0
	for _, d := range ds {
		if str(d, "state") == state {
			n++
		}
	}
	return n
}

func webhookDown(r *Run) error {
	rc, err := newReceiver(http.StatusServiceUnavailable)
	if err != nil {
		return err
	}
	defer rc.Close()
	defer setenv("SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS", "127.0.0.0/8")()
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	created, err := c.Must(r.Ctx, http.MethodPost, "/webhooks-out", map[string]any{"url": rc.URL, "events": []string{"agent.updated"}}, http.StatusCreated)
	if err != nil {
		return err
	}
	hook := str(created, "webhook", "id")
	register := func(id string) error {
		_, err := c.Must(r.Ctx, http.MethodPost, "/agents", map[string]any{"id": id, "role": "worker", "kind": "claude-code", "model": "gameday"}, http.StatusCreated)
		return err
	}

	start := time.Now()
	if err := register("gd-hook-a"); err != nil {
		return err
	}
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) {
		ds, err := deliveries(r, c, hook)
		if err != nil || len(ds) == 0 {
			return false, err
		}
		for _, d := range ds {
			if num(d, "attempts") < 1 || num(d, "last_status") != http.StatusServiceUnavailable {
				return false, nil
			}
		}
		return true, nil
	})
	if !r.Expect("failed attempt recorded", ok, "attempts with status 503 on every delivery") || !r.Detected(time.Since(start)) {
		return nil
	}
	ds, err := deliveries(r, c, hook)
	if err != nil {
		return err
	}
	r.Expect("failed deliveries kept for retry", countState(ds, "pending") == len(ds), "%d pending of %d", countState(ds, "pending"), len(ds))
	before := rc.hits()
	r.Sleep(600 * time.Millisecond)
	r.Expect("retries back off", rc.hits() == before, "%d attempts before, %d after waiting", before, rc.hits())

	st.Clock.Jump(time.Hour)
	_, ok = r.Until(r.Opt.Detect, func() (bool, error) { return rc.hits() > before, nil })
	r.Expect("retry after the backoff elapsed", ok, "receiver saw another attempt")

	rc.status.Store(http.StatusOK)
	restored := time.Now()
	for range 6 {
		st.Clock.Jump(time.Hour)
		if _, ok := r.Until(2*time.Second, func() (bool, error) {
			ds, err := deliveries(r, c, hook)
			return err == nil && countState(ds, "delivered") == len(ds), err
		}); ok {
			break
		}
	}
	ds, err = deliveries(r, c, hook)
	if err != nil {
		return err
	}
	if r.Expect("delivered once the receiver is back", len(ds) > 0 && countState(ds, "delivered") == len(ds), "%d delivered of %d", countState(ds, "delivered"), len(ds)) {
		r.Recovered(time.Since(restored))
	}

	rc.status.Store(http.StatusServiceUnavailable)
	if err := register("gd-hook-b"); err != nil {
		return err
	}
	var dead []map[string]any
	for range 14 {
		seen := rc.hits()
		st.Clock.Jump(5 * time.Hour)
		r.Until(2*time.Second, func() (bool, error) {
			ds, err := deliveries(r, c, hook)
			return rc.hits() > seen || (err == nil && countState(ds, "pending") == 0), err
		})
		if ds, err = deliveries(r, c, hook); err != nil {
			return err
		}
		if countState(ds, "pending") == 0 && countState(ds, "dead") > 0 {
			break
		}
	}
	for _, d := range ds {
		if str(d, "state") == "dead" {
			dead = append(dead, d)
		}
	}
	if !r.Expect("undeliverable events dead-lettered", len(dead) > 0 && countState(ds, "pending") == 0, "%d dead, %d pending", len(dead), countState(ds, "pending")) {
		return nil
	}
	first := dead[0]
	r.Expect("dead letter keeps the reason", num(first, "last_status") == http.StatusServiceUnavailable && str(first, "last_error") != "", "status %v, error %q", first["last_status"], str(first, "last_error"))
	r.Expect("receiver can dedupe on the delivery id", rc.attempts(str(first, "id")) >= 2, "%d attempts with id %s", rc.attempts(str(first, "id")), str(first, "id"))
	r.Expect("payload carries no token", !strings.Contains(fmt.Sprint(first["payload"]), "ssp_"), "payload checked")

	rc.status.Store(http.StatusOK)
	for _, d := range dead {
		if _, err := c.Must(r.Ctx, http.MethodPost, "/webhooks-out/"+hook+"/deliveries/"+str(d, "id")+"/redeliver", map[string]any{}, http.StatusOK); err != nil {
			return err
		}
	}
	_, ok = r.Until(r.Opt.Recover, func() (bool, error) {
		ds, err := deliveries(r, c, hook)
		return err == nil && countState(ds, "delivered") == len(ds), err
	})
	r.Expect("redelivery reaches the receiver", ok, "all deliveries delivered")
	return nil
}

func backupCorruption(r *Run) error {
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
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	snap, err := c.Must(r.Ctx, http.MethodPost, "/backup", map[string]any{}, http.StatusOK)
	if err != nil {
		return err
	}
	good, err := c.Must(r.Ctx, http.MethodPost, "/drills", map[string]any{}, http.StatusCreated)
	if err != nil {
		return err
	}
	if !r.Expect("drill of an intact backup passes", str(good, "status") == "ok", "status %s %s", str(good, "status"), str(good, "error")) {
		return nil
	}
	if err := corrupt(str(snap, "path")); err != nil {
		return err
	}

	start := time.Now()
	bad, err := c.Must(r.Ctx, http.MethodPost, "/drills", map[string]any{}, http.StatusCreated)
	if err != nil {
		return err
	}
	if !r.Expect("drill of a corrupt backup fails", str(bad, "status") == "failed", "status %s", str(bad, "status")) {
		return nil
	}
	alert := "drill:" + str(bad, "id")
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) { return hasDecision(r, c, alert) })
	if !r.Expect("owner alert raised", ok, "decision %s", alert) || !r.Detected(time.Since(start)) {
		return nil
	}
	live, err := c.Must(r.Ctx, http.MethodGet, "/state", nil, http.StatusOK)
	if err != nil {
		return err
	}
	r.Expect("live state unaffected", live["paused"] == false, "state served, not paused")

	fixing := time.Now()
	st.Clock.Jump(2 * time.Second)
	if _, err := c.Must(r.Ctx, http.MethodPost, "/backup", map[string]any{}, http.StatusOK); err != nil {
		return err
	}
	fresh, err := c.Must(r.Ctx, http.MethodPost, "/drills", map[string]any{}, http.StatusCreated)
	if err != nil {
		return err
	}
	r.Expect("drill of a fresh backup passes", str(fresh, "status") == "ok", "status %s %s", str(fresh, "status"), str(fresh, "error"))
	_, ok = r.Until(r.Opt.Recover, func() (bool, error) {
		has, err := hasDecision(r, c, alert)
		return !has, err
	})
	if r.Expect("alert resolved by the passing drill", ok, "decision %s gone", alert) {
		r.Recovered(time.Since(fixing))
	}
	return nil
}

func corrupt(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // path reported by the backup API of the stack under test
	if err != nil {
		return err
	}
	for i := 100; i < len(b); i++ {
		b[i] = 0xA5
	}
	return os.WriteFile(path, b, 0o600) //nolint:gosec // path reported by the backup API of the stack under test
}

func hasDecision(r *Run, c *Client, id string) (bool, error) {
	out, err := c.Must(r.Ctx, http.MethodGet, "/decisions?limit=1000", nil, http.StatusOK)
	if err != nil {
		return false, err
	}
	for _, d := range list(out, "decisions") {
		if str(d, "id") == id {
			return true, nil
		}
	}
	return false, nil
}

func leaderLoss(r *Run) error {
	nodes, closeAll, err := r.Opt.NewCluster(2)
	if err != nil {
		return err
	}
	defer closeAll()
	clients := []*Client{nodes[0].Client(), nodes[1].Client()}
	leaders := func() ([]int, bool, error) {
		var out []int
		agreed := true
		for i, n := range nodes {
			if !n.Up() {
				continue
			}
			st, err := clients[i].Must(r.Ctx, http.MethodGet, "/ha", nil, http.StatusOK)
			if err != nil {
				return nil, false, err
			}
			if st["leader"] == true {
				out = append(out, i)
			}
			agreed = agreed && st["leader_instance"] != nil
		}
		return out, agreed, nil
	}
	lead := -1
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) {
		ls, agreed, err := leaders()
		if len(ls) == 1 && agreed {
			lead = ls[0]
		}
		return lead >= 0, err
	})
	if !r.Expect("one leader elected", ok, "leader node %d", lead) {
		return nil
	}
	other := 1 - lead
	w, err := clients[other].Seed(r.Ctx, 2)
	if err != nil {
		return err
	}
	if _, err := clients[other].MustTool(r.Ctx, w.Workers[0].Token, "claim_task", map[string]any{"task_id": w.Tasks[0]}); err != nil {
		return err
	}

	killed := time.Now()
	if err := nodes[lead].Kill(); err != nil {
		return err
	}
	_, ok = r.Until(r.Opt.Detect, func() (bool, error) {
		ls, _, err := leaders()
		return len(ls) == 1 && ls[0] == other, err
	})
	if !r.Expect("survivor took over leadership", ok, "node %d leads", other) || !r.Detected(time.Since(killed)) {
		return nil
	}
	_, ok = r.Until(r.Opt.Recover+r.Opt.Lease, func() (bool, error) {
		t, err := clients[other].Must(r.Ctx, http.MethodGet, "/tasks/"+w.Tasks[0], nil, http.StatusOK)
		return err == nil && str(t, "status") == "assigned", err
	})
	if r.Expect("new leader runs the lease sweeper", ok, "unrenewed lease expired") {
		r.Recovered(time.Since(killed))
	}
	if _, err := clients[other].MustTool(r.Ctx, w.Workers[1].Token, "post_message", map[string]any{"thread": "task:" + w.Tasks[1], "body": "after failover"}); err != nil {
		return err
	}
	t, err := clients[other].Must(r.Ctx, http.MethodGet, "/tasks/"+w.Tasks[1], nil, http.StatusOK)
	if err != nil {
		return err
	}
	r.Expect("data and tokens survive failover", str(t, "assignee") == w.Workers[1].ID, "assignee %s", str(t, "assignee"))
	ok, detail, err := clients[other].AuditOK(r.Ctx)
	if err != nil {
		return err
	}
	r.Expect("audit chain verifies", ok, "%s", detail)
	return nil
}
