package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Status string

const (
	Pass Status = "pass"
	Fail Status = "fail"
	Skip Status = "skip"
)

type Mode int

const (
	Anywhere Mode = iota
	Disruptive
	Local
)

type Options struct {
	URL        string
	Token      string
	Disruptive bool
	Detect     time.Duration
	Recover    time.Duration
	Lease      time.Duration
	Agents     int
	Subs       int
	Burst      int
	Storm      time.Duration
	Seed       uint64
	PGDSN      string
	Log        *slog.Logger
}

func (o Options) remote() bool { return o.URL != "" }

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Result struct {
	Name       string  `json:"name"`
	Status     Status  `json:"status"`
	DetectMS   *int64  `json:"detect_ms"`
	RecoverMS  *int64  `json:"recover_ms"`
	DurationMS int64   `json:"duration_ms"`
	Checks     []Check `json:"checks"`
	Error      string  `json:"error,omitempty"`
	Note       string  `json:"note,omitempty"`
}

type Scenario struct {
	Name string
	Desc string
	Mode Mode
	PG   bool
	Run  func(*Run) error
}

type Run struct {
	Ctx context.Context
	Opt Options
	res *Result
}

func (r *Run) Expect(name string, ok bool, format string, args ...any) bool {
	r.res.Checks = append(r.res.Checks, Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
	return ok
}

func (r *Run) Detected(d time.Duration) bool {
	ms := d.Milliseconds()
	r.res.DetectMS = &ms
	return r.Expect("detected within limit", d <= r.Opt.Detect, "%s, limit %s", d.Round(time.Millisecond), r.Opt.Detect)
}

func (r *Run) Recovered(d time.Duration) bool {
	ms := d.Milliseconds()
	r.res.RecoverMS = &ms
	return r.Expect("recovered within limit", d <= r.Opt.Recover, "%s, limit %s", d.Round(time.Millisecond), r.Opt.Recover)
}

func (r *Run) Until(limit time.Duration, fn func() (bool, error)) (time.Duration, bool) {
	start := time.Now()
	for {
		ok, err := fn()
		if err == nil && ok {
			return time.Since(start), true
		}
		if time.Since(start) >= limit || r.Ctx.Err() != nil {
			return time.Since(start), false
		}
		select {
		case <-r.Ctx.Done():
			return time.Since(start), false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (r *Run) Sleep(d time.Duration) {
	select {
	case <-r.Ctx.Done():
	case <-time.After(d):
	}
}

func execute(ctx context.Context, opt Options, s Scenario) Result {
	res := Result{Name: s.Name, Status: Pass, Checks: []Check{}}
	if reason := skipReason(opt, s); reason != "" {
		res.Status, res.Note = Skip, reason
		return res
	}
	start := time.Now()
	r := &Run{Ctx: ctx, Opt: opt, res: &res}
	if err := s.Run(r); err != nil {
		res.Status, res.Error = Fail, err.Error()
	}
	for _, c := range res.Checks {
		if !c.OK {
			res.Status = Fail
		}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

func skipReason(opt Options, s Scenario) string {
	switch {
	case s.PG && opt.PGDSN == "":
		return "SUBSPACE_TEST_PG_DSN is not set"
	case s.Mode == Local && opt.remote():
		return "needs an in-process stack"
	case s.Mode == Disruptive && opt.remote() && !opt.Disruptive:
		return "pauses or reconfigures the target, pass -disruptive"
	}
	return ""
}

func runAll(ctx context.Context, opt Options, list []Scenario) []Result {
	out := make([]Result, 0, len(list))
	for _, s := range list {
		if ctx.Err() != nil {
			break
		}
		opt.Log.Info("scenario", "name", s.Name)
		out = append(out, execute(ctx, opt, s))
	}
	return out
}

func passed(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return false
		}
	}
	return true
}
