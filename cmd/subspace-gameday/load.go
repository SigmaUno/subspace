package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Sample struct {
	Start time.Time
	Dur   time.Duration
	OK    bool
}

func (s Sample) End() time.Time { return s.Start.Add(s.Dur) }

type ack struct {
	task string
	body string
}

type Load struct {
	mu      sync.Mutex
	samples []Sample
	acked   []ack
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func StartLoad(ctx context.Context, c *Client, w *Work, every time.Duration) *Load {
	ctx, cancel := context.WithCancel(ctx)
	l := &Load{cancel: cancel}
	for i := range w.Workers {
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.loop(ctx, c, w.Workers[i], w.Tasks[i], every)
		}()
	}
	return l
}

func (l *Load) loop(ctx context.Context, c *Client, a Agent, task string, every time.Duration) {
	for n := 0; ctx.Err() == nil; n++ {
		body := fmt.Sprintf("gameday %s %d", a.ID, n)
		if l.call(ctx, c, a, "heartbeat", map[string]any{"task_id": task}, nil) {
			l.call(ctx, c, a, "post_message", map[string]any{"thread": "task:" + task, "body": body}, &ack{task, body})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (l *Load) call(ctx context.Context, c *Client, a Agent, name string, args map[string]any, done *ack) bool {
	start := time.Now()
	_, code, err := c.Tool(ctx, a.Token, name, args)
	if ctx.Err() != nil {
		return false
	}
	ok := err == nil && code == ""
	l.mu.Lock()
	l.samples = append(l.samples, Sample{Start: start, Dur: time.Since(start), OK: ok})
	if ok && done != nil {
		l.acked = append(l.acked, *done)
	}
	l.mu.Unlock()
	return ok
}

func (l *Load) Stop() {
	l.cancel()
	l.wg.Wait()
}

func (l *Load) Samples() []Sample {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Sample(nil), l.samples...)
}

func (l *Load) Acked() []ack {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ack(nil), l.acked...)
}

func (l *Load) Failures() int {
	n := 0
	for _, s := range l.Samples() {
		if !s.OK {
			n++
		}
	}
	return n
}

func (l *Load) FirstFailure(after time.Time) (time.Time, bool) {
	for _, s := range l.Samples() {
		if !s.OK && s.End().After(after) {
			return s.End(), true
		}
	}
	return time.Time{}, false
}

func (l *Load) FirstSuccess(after time.Time) (time.Time, bool) {
	for _, s := range l.Samples() {
		if s.OK && s.Start.After(after) {
			return s.End(), true
		}
	}
	return time.Time{}, false
}

func (l *Load) FirstSlow(after time.Time, slow time.Duration) (time.Time, bool) {
	for _, s := range l.Samples() {
		if s.End().After(after) && (!s.OK || s.Dur >= slow) {
			at := s.Start.Add(min(s.Dur, slow))
			return at, true
		}
	}
	return time.Time{}, false
}

func (l *Load) FirstFast(after time.Time, slow time.Duration) (time.Time, bool) {
	for _, s := range l.Samples() {
		if s.OK && s.Dur < slow && s.Start.After(after) {
			return s.End(), true
		}
	}
	return time.Time{}, false
}

func (l *Load) Max() time.Duration {
	var m time.Duration
	for _, s := range l.Samples() {
		m = max(m, s.Dur)
	}
	return m
}

func missingAcked(ctx context.Context, c *Client, acked []ack) (int, error) {
	byTask := map[string]map[string]bool{}
	for _, a := range acked {
		byTask[a.task] = map[string]bool{}
	}
	for task := range byTask {
		var cursor string
		for {
			path := "/threads/task/" + task + "/messages?limit=1000"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			out, err := c.Must(ctx, http.MethodGet, path, nil, http.StatusOK)
			if err != nil {
				return 0, err
			}
			for _, m := range list(out, "messages") {
				byTask[task][str(m, "body")] = true
			}
			cursor = str(out, "next_cursor")
			if cursor == "" {
				break
			}
		}
	}
	missing := 0
	for _, a := range acked {
		if !byTask[a.task][a.body] {
			missing++
		}
	}
	return missing, nil
}
