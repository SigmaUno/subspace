package prsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/retryq"
)

const maxBackoffShift = 5

var actor = model.Actor{ID: "prsync", Role: "system"}

var polled = strings.Join([]string{model.StatusSubmitted, model.StatusInReview, model.StatusApproved}, ",")

type Options struct {
	Config     Config
	Board      *board.Board
	Store      Store
	Hub        events.Publisher
	Log        *slog.Logger
	Clock      ratelimit.Clock
	Rand       *rand.Rand
	HTTPClient *http.Client
}

type retry struct {
	fails int
	next  time.Time
}

type Poller struct {
	cfg   Config
	b     *board.Board
	st    Store
	hub   events.Publisher
	log   *slog.Logger
	clock ratelimit.Clock
	rnd   *rand.Rand
	f     *fetcher
	hosts map[string]*host
	byWeb map[string]*host

	mu        sync.Mutex
	last      map[string]Status
	retries   map[string]*retry
	hostUntil map[string]time.Time
}

func New(o Options) (*Poller, error) {
	if o.Board == nil || o.Store == nil {
		return nil, errors.New("prsync: board and store are required")
	}
	if o.Config.Interval <= 0 {
		o.Config.Interval = DefaultInterval
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = retryq.SystemClock{}
	}
	if o.Rand == nil {
		o.Rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // backoff jitter
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	limits := map[string]ratelimit.Limit{globalKey: {PerMinute: o.Config.Rate.PerMinute, Burst: o.Config.Rate.Burst}}
	p := &Poller{
		cfg: o.Config, b: o.Board, st: o.Store, hub: o.Hub, log: o.Log, clock: o.Clock, rnd: o.Rand,
		hosts: map[string]*host{}, byWeb: map[string]*host{},
		last: map[string]Status{}, retries: map[string]*retry{}, hostUntil: map[string]time.Time{},
	}
	for name, hc := range o.Config.Hosts {
		u, err := url.Parse(hc.URL)
		if err != nil {
			return nil, fmt.Errorf("prsync: host %q: invalid url", name)
		}
		h := &host{name: name, kind: hc.Kind, web: strings.ToLower(u.Host), api: hc.APIURL, token: hc.Token, authors: hc.Authors}
		p.hosts[name] = h
		if _, dup := p.byWeb[h.web]; !dup {
			p.byWeb[h.web] = h
		}
		if hc.Rate != nil {
			limits[hostKey(name)] = ratelimit.Limit{PerMinute: hc.Rate.PerMinute, Burst: hc.Rate.Burst}
		}
	}
	p.f = &fetcher{
		http: o.HTTPClient, clock: o.Clock, cache: map[string]*cached{},
		limits: ratelimit.New(ratelimit.Options{Limits: limits, Clock: o.Clock, IdleTTL: -1}),
	}
	return p, nil
}

func (p *Poller) Run(ctx context.Context) {
	for {
		if err := p.SyncOnce(ctx); err != nil && ctx.Err() == nil {
			p.log.Warn("pr sync cycle failed", "err", err)
		}
		ch, stop := p.clock.Timer(p.cfg.Interval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-ch:
		}
	}
}

func (p *Poller) SyncOnce(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	tasks, err := p.b.ListTasks(ctx, board.TaskQuery{Status: polled, Limit: 100000})
	if err != nil {
		return err
	}
	if err := p.track(ctx, tasks); err != nil {
		return err
	}
	now := p.clock.Now()
	tasks = slices.DeleteFunc(tasks, func(t *model.Task) bool {
		r := p.retries[t.ID]
		return t.PRURL == nil || *t.PRURL == "" || (r != nil && now.Before(r.next))
	})
	slices.SortStableFunc(tasks, func(a, b *model.Task) int { return p.lastChecked(a.ID).Compare(p.lastChecked(b.ID)) })
	var errs []error
	for _, t := range tasks {
		if err := p.syncTask(ctx, t, now); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.ID, err))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	p.f.prune(now.Add(-10 * p.cfg.Interval))
	return errors.Join(errs...)
}

func (p *Poller) lastChecked(taskID string) time.Time {
	if c := p.last[taskID].CheckedAt; c != nil {
		return *c
	}
	return time.Time{}
}

func (p *Poller) track(ctx context.Context, tasks []*model.Task) error {
	polled := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		polled[t.ID] = true
	}
	for id := range p.last {
		if !polled[id] {
			delete(p.last, id)
		}
	}
	for id := range p.retries {
		if !polled[id] {
			delete(p.retries, id)
		}
	}
	for _, t := range tasks {
		if _, ok := p.last[t.ID]; ok || t.PRURL == nil || *t.PRURL == "" {
			continue
		}
		raw, ok, err := p.st.PRStatus(ctx, t.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		var s Status
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		p.last[t.ID] = s
	}
	return nil
}

func (p *Poller) hostFor(repo, prURL string) *host {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil
	}
	web := strings.ToLower(u.Host)
	if name, ok := p.cfg.Repos[strings.ToLower(repo)]; ok {
		if h := p.hosts[name]; h != nil && h.web == web {
			return h
		}
		return nil
	}
	return p.byWeb[web]
}

func (p *Poller) syncTask(ctx context.Context, t *model.Task, now time.Time) error {
	base := Status{TaskID: t.ID, BriefID: t.BriefID, URL: *t.PRURL, Reviews: []Review{}}
	repo, number, err := githost.ParsePRURL(*t.PRURL)
	if err != nil {
		return p.failed(ctx, base, "invalid pull request url", now)
	}
	base.Repo, base.Number = repo, number
	if !strings.EqualFold(repo, t.Repo) {
		return p.failed(ctx, base, "pull request is not in the task repo "+t.Repo, now)
	}
	h := p.hostFor(repo, *t.PRURL)
	if h == nil {
		return p.failed(ctx, base, "no git host configured for "+repo, now)
	}
	base.Host = h.name
	if now.Before(p.hostUntil[h.name]) {
		return nil
	}
	prev, had := p.last[t.ID]
	if had && prev.URL != base.URL {
		prev, had = Status{}, false
	}
	var prevPtr *Status
	if had {
		prevPtr = &prev
	}
	pr, err := p.f.fetch(ctx, h, repo, number, prevPtr)
	if err != nil {
		if errors.Is(err, errLimited) || errors.Is(err, errStale) || ctx.Err() != nil {
			return nil
		}
		var he *hostError
		if errors.As(err, &he) && he.throttled {
			wait := he.retryAfter
			if wait <= 0 {
				wait = p.delay(1)
			}
			p.hostUntil[h.name] = now.Add(wait)
			p.log.Warn("pr sync host rate limited", "host", h.name, "retry_in", wait)
			return nil
		}
		msg := err.Error()
		if errors.Is(err, githost.ErrNotFound) {
			msg = "pull request not found"
		}
		p.log.Warn("pr sync fetch failed", "task", t.ID, "host", h.name, "repo", repo, "err", msg)
		return p.failed(ctx, base, msg, now)
	}
	delete(p.retries, t.ID)

	next := base
	next.State, next.Draft, next.Checks, next.Mergeable = pr.state, pr.draft, pr.checks, pr.mergeable
	next.HeadSHA, next.Reviews, next.ReviewDecision = pr.headSHA, pr.reviews, pr.decision
	next.Diff = pr.diff
	next.Scope = scoreTask(t, h, pr.diff)
	next.MergeReady = next.ready(t.Status)
	next.CheckedAt = &now
	if err := p.save(ctx, prev, had, next, now, true); err != nil {
		return err
	}
	return p.transitions(ctx, t, prev, next, pr.forcePushed)
}

func (p *Poller) failed(ctx context.Context, base Status, msg string, now time.Time) error {
	r := p.retries[base.TaskID]
	if r == nil {
		r = &retry{}
		p.retries[base.TaskID] = r
	}
	r.fails++
	r.next = now.Add(p.delay(r.fails))
	prev, had := p.last[base.TaskID]
	next := base
	if had && prev.URL == base.URL {
		next = prev
	}
	next.Error = &msg
	return p.save(ctx, prev, had, next, now, false)
}

func (p *Poller) delay(fails int) time.Duration {
	return retryq.HalfJitter(retryq.Exp(p.cfg.Interval, p.cfg.Interval<<maxBackoffShift, fails), p.rnd.Int64N)
}

func (p *Poller) save(ctx context.Context, prev Status, had bool, next Status, now time.Time, checked bool) error {
	changed := !had || !prev.same(next)
	if !changed && !checked {
		return nil
	}
	next.ChangedAt = prev.ChangedAt
	if changed {
		next.ChangedAt = now
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := p.st.SetPRStatus(ctx, next.TaskID, raw); err != nil {
		return err
	}
	p.last[next.TaskID] = next
	if changed && p.hub != nil {
		p.hub.Publish([]string{"board", "brief:" + next.BriefID, "task:" + next.TaskID}, EventPR, next)
	}
	return nil
}

func (p *Poller) transitions(ctx context.Context, t *model.Task, prev, next Status, forcePushed bool) error {
	var msgs []string
	if forcePushed {
		msgs = append(msgs, fmt.Sprintf("Pull request #%d was force-pushed: head %s replaced by %s: %s", next.Number, short(prev.HeadSHA), short(next.HeadSHA), next.URL))
	}
	switch {
	case next.State == StateMerged && prev.State != StateMerged:
		return errors.Join(p.post(ctx, t.ID, msgs), p.merged(ctx, t, next))
	case next.State == StateClosed && prev.State != StateClosed:
		msgs = append(msgs, fmt.Sprintf("Warning: pull request #%d was closed without merging: %s", next.Number, next.URL))
	case next.State == StateOpen && next.Checks == ChecksFailure && (prev.Checks != ChecksFailure || prev.HeadSHA != next.HeadSHA):
		msgs = append(msgs, fmt.Sprintf("Checks failed on pull request #%d at %s: %s", next.Number, short(next.HeadSHA), next.URL))
	}
	return p.post(ctx, t.ID, msgs)
}

func (p *Poller) merged(ctx context.Context, t *model.Task, s Status) error {
	note := fmt.Sprintf("Pull request #%d merged: %s", s.Number, s.URL)
	br, err := p.b.GetBrief(ctx, t.BriefID)
	if err != nil {
		return err
	}
	if !br.AutoDoneOnMerge {
		return p.post(ctx, t.ID, []string{note})
	}
	_, err = p.b.MarkDone(ctx, t.ID, note)
	if model.AsError(err).Code == "conflict" {
		return p.post(ctx, t.ID, []string{fmt.Sprintf("%s. Task was not marked done: %s", note, model.AsError(err).Message)})
	}
	return err
}

func (p *Poller) post(ctx context.Context, taskID string, msgs []string) error {
	var errs []error
	for _, body := range msgs {
		_, err := p.b.AppendMessage(ctx, model.ThreadTask, taskID, model.Message{
			AuthorID: actor.ID, AuthorRole: actor.Role, Kind: model.MsgSystem, Body: body,
		})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
