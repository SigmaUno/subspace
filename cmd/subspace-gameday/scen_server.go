package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"time"
)

const slowCall = 200 * time.Millisecond

func (r *Run) workers() int { return max(r.Opt.Agents, 2) }

func serverRestart(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, r.workers())
	if err != nil {
		return err
	}
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	lc := c.With(st.URL, 2*time.Second)
	load := StartLoad(r.Ctx, lc, w, 10*time.Millisecond)
	defer load.Stop()
	r.Sleep(400 * time.Millisecond)

	killed := time.Now()
	if err := st.Kill(); err != nil {
		return err
	}
	if _, ok := r.Until(r.Opt.Detect, func() (bool, error) { _, ok := load.FirstFailure(killed); return ok, nil }); !ok {
		r.Expect("outage seen by agents", false, "no request failed after the kill")
	} else if at, _ := load.FirstFailure(killed); !r.Detected(at.Sub(killed)) {
		return nil
	}
	before := len(load.Acked())
	r.Sleep(300 * time.Millisecond)

	restarted := time.Now()
	if err := st.Start(); err != nil {
		return err
	}
	_, ok := r.Until(r.Opt.Recover, func() (bool, error) { _, ok := load.FirstSuccess(restarted); return ok, nil })
	if r.Expect("agents served again", ok, "first success after restart") {
		at, _ := load.FirstSuccess(restarted)
		r.Recovered(at.Sub(restarted))
	}
	r.Sleep(500 * time.Millisecond)
	load.Stop()

	acked := load.Acked()
	r.Expect("agents kept working after restart", len(acked) > before, "%d acknowledged writes before the kill, %d in total", before, len(acked))
	missing, err := missingAcked(r.Ctx, c, acked)
	if err != nil {
		return err
	}
	r.Expect("no acknowledged write lost", missing == 0, "%d of %d missing", missing, len(acked))
	for i := range w.Tasks {
		t, err := w.Task(r.Ctx, i)
		if err != nil {
			return err
		}
		s := str(t, "status")
		r.Expect("task "+w.Tasks[i]+" intact", s == "in_progress" || s == "assigned", "status %s", s)
	}
	ok, detail, err := c.AuditOK(r.Ctx)
	if err != nil {
		return err
	}
	r.Expect("audit chain verifies", ok, "%s", detail)
	return nil
}

func dbBusyStorm(r *Run) error {
	storm := r.Opt.Storm
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, r.workers())
	if err != nil {
		return err
	}
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	lc := c.With(st.URL, storm+10*time.Second)
	load := StartLoad(r.Ctx, lc, w, 10*time.Millisecond)
	defer load.Stop()
	r.Sleep(300 * time.Millisecond)

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(st.Cfg.DBPath)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(r.Ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	const cycles = 3
	var readSlow int
	var detect, recoverIn time.Duration
	for i := range cycles {
		if _, err := conn.ExecContext(r.Ctx, "BEGIN IMMEDIATE"); err != nil {
			return fmt.Errorf("take write lock: %w", err)
		}
		locked := time.Now()
		for time.Since(locked) < storm {
			t0 := time.Now()
			code, _, err := c.REST(r.Ctx, http.MethodGet, "/state", nil)
			if err != nil || code != http.StatusOK || time.Since(t0) > time.Second {
				readSlow++
			}
			r.Sleep(50 * time.Millisecond)
		}
		if _, err := conn.ExecContext(r.Ctx, "ROLLBACK"); err != nil {
			return err
		}
		released := time.Now()
		if i == 0 {
			_, seen := r.Until(r.Opt.Recover, func() (bool, error) { _, ok := load.FirstSlow(locked, slowCall); return ok, nil })
			if seen {
				at, _ := load.FirstSlow(locked, slowCall)
				detect = at.Sub(locked)
			} else {
				r.Expect("storm visible to agents", false, "no request was slowed or failed")
			}
		}
		d, ok := r.Until(r.Opt.Recover, func() (bool, error) { _, ok := load.FirstFast(released, slowCall); return ok, nil })
		r.Expect("writes fast again after the storm", ok, "cycle %d", i+1)
		recoverIn = max(recoverIn, d)
		r.Sleep(300 * time.Millisecond)
	}
	if detect > 0 && !r.Detected(detect) {
		return nil
	}
	r.Recovered(recoverIn)
	load.Stop()
	r.Expect("reads stay available during write lock", readSlow == 0, "%d slow or failed reads", readSlow)
	r.Expect("no request failed", load.Failures() == 0, "%d failed, slowest %s", load.Failures(), load.Max().Round(time.Millisecond))
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

func clockJump(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, 2)
	if err != nil {
		return err
	}
	if err := w.ClaimAll(r.Ctx); err != nil {
		return err
	}
	const jump = time.Hour

	jumped := time.Now()
	st.Clock.Jump(jump)
	d, ok := r.Until(r.Opt.Detect, func() (bool, error) { return allTasks(r.Ctx, w, "assigned") })
	if !r.Expect("expired leases noticed after the jump", ok, "both tasks back to assigned") || !r.Detected(d) {
		return nil
	}
	for i, a := range w.Workers {
		if _, err := c.MustTool(r.Ctx, a.Token, "claim_task", map[string]any{"task_id": w.Tasks[i]}); err != nil {
			return err
		}
	}
	r.Recovered(time.Since(jumped))
	for i := range w.Tasks {
		t, err := w.Task(r.Ctx, i)
		if err != nil {
			return err
		}
		r.Expect("task "+w.Tasks[i]+" claimed again", str(t, "status") == "in_progress" && num(t, "lease_expiries") == 1, "status %s, %v expiries", str(t, "status"), t["lease_expiries"])
	}

	st.Clock.Jump(-2 * jump)
	r.Sleep(500 * time.Millisecond)
	for i, a := range w.Workers {
		if _, err := c.MustTool(r.Ctx, a.Token, "heartbeat", map[string]any{"task_id": w.Tasks[i]}); err != nil {
			return err
		}
		if _, err := c.MustTool(r.Ctx, a.Token, "post_message", map[string]any{"thread": "task:" + w.Tasks[i], "body": "after the clock went back"}); err != nil {
			return err
		}
	}
	held, err := allTasks(r.Ctx, w, "in_progress")
	if err != nil {
		return err
	}
	r.Expect("leases survive a backwards jump", held, "tasks still in_progress")
	for _, a := range w.Workers {
		out, err := c.Must(r.Ctx, http.MethodGet, "/agents/"+a.ID, nil, http.StatusOK)
		if err != nil {
			return err
		}
		r.Expect("agent "+a.ID+" not paused", str(out, "status") == "active", "status %s", str(out, "status"))
	}
	ok, detail, err := c.AuditOK(r.Ctx)
	if err != nil {
		return err
	}
	r.Expect("audit chain verifies", ok, "%s", detail)
	return nil
}

func allTasks(ctx context.Context, w *Work, status string) (bool, error) {
	for i := range w.Tasks {
		t, err := w.Task(ctx, i)
		if err != nil {
			return false, err
		}
		if str(t, "status") != status {
			return false, nil
		}
	}
	return true, nil
}
