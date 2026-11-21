package forgestatus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/githost/forgejo"
	"github.com/SigmaUno/subspace/internal/githost/github"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/retryq"
)

const (
	Context = "subspace/review"

	globalKey = "global"
	workers   = 4
	maxPosted = 10000
)

type Client interface {
	HeadSHA(ctx context.Context, repo string, number int64) (string, error)
	PostStatus(ctx context.Context, repo, sha string, s githost.CommitStatus) error
}

type Options struct {
	Config     prsync.Config
	PublicURL  string
	Log        *slog.Logger
	Clock      ratelimit.Clock
	HTTPClient *http.Client
	Attempts   int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	MaxQueued  int
}

type target struct {
	name   string
	web    string
	client Client
}

type Service struct {
	cfg     Options
	log     *slog.Logger
	clock   ratelimit.Clock
	limiter *ratelimit.Limiter
	hosts   map[string]*target
	byWeb   map[string]*target
	repos   map[string]string
	public  string

	wake chan struct{}
	wg   sync.WaitGroup

	mu       sync.Mutex
	pending  map[string]*model.Task
	order    []string
	inflight map[string]bool
	sent     map[string]string
	posted   map[string]string
}

func hostKey(name string) string { return "host:" + name }

func New(o Options) (*Service, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = retryq.SystemClock{}
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Attempts <= 0 {
		o.Attempts = 4
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 30 * time.Second
	}
	if o.MaxQueued <= 0 {
		o.MaxQueued = 4096
	}
	limits := map[string]ratelimit.Limit{globalKey: {PerMinute: o.Config.Rate.PerMinute, Burst: o.Config.Rate.Burst}}
	s := &Service{
		cfg: o, log: o.Log, clock: o.Clock,
		hosts: map[string]*target{}, byWeb: map[string]*target{}, repos: o.Config.Repos,
		wake:    make(chan struct{}, 1),
		pending: map[string]*model.Task{}, inflight: map[string]bool{},
		sent: map[string]string{}, posted: map[string]string{},
	}
	if strings.HasPrefix(o.PublicURL, "https://") {
		s.public = strings.TrimRight(o.PublicURL, "/")
	}
	for name, hc := range o.Config.Hosts {
		u, err := url.Parse(hc.URL)
		if err != nil {
			return nil, fmt.Errorf("forgestatus: host %q: invalid url", name)
		}
		c, err := newClient(hc, o.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("forgestatus: host %q: %w", name, err)
		}
		t := &target{name: name, web: strings.ToLower(u.Host), client: c}
		s.hosts[name] = t
		if _, dup := s.byWeb[t.web]; !dup {
			s.byWeb[t.web] = t
		}
		if hc.Rate != nil {
			limits[hostKey(name)] = ratelimit.Limit{PerMinute: hc.Rate.PerMinute, Burst: hc.Rate.Burst}
		}
	}
	s.limiter = ratelimit.New(ratelimit.Options{Limits: limits, Clock: o.Clock, IdleTTL: -1})
	return s, nil
}

func (s *Service) Start(ctx context.Context, hub events.Hub) {
	sub := hub.Subscribe([]string{"board"})
	s.wg.Go(func() {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.C():
				if ev.Type == "task.created" || ev.Type == "task.updated" {
					s.Enqueue(ev.Data)
				}
			}
		}
	})
	for range workers {
		s.wg.Go(func() { s.work(ctx) })
	}
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) Enqueue(data any) {
	var t model.Task
	switch d := data.(type) {
	case *model.Task:
		if d == nil {
			return
		}
		t = *d
	case model.Task:
		t = d
	default:
		return
	}
	if _, _, ok := Map(t.Status); !ok || t.ID == "" || t.PRURL == nil || *t.PRURL == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent[t.ID] == taskKey(&t) {
		return
	}
	if _, queued := s.pending[t.ID]; !queued {
		if len(s.order) >= s.cfg.MaxQueued {
			delete(s.pending, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, t.ID)
	}
	s.pending[t.ID] = &t
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func Map(status string) (state, description string, ok bool) {
	switch status {
	case model.StatusSubmitted:
		return githost.StatePending, "Awaiting Subspace review", true
	case model.StatusInReview:
		return githost.StatePending, "Subspace review in progress", true
	case model.StatusChangesRequested:
		return githost.StateFailure, "Subspace reviewer requested changes", true
	case model.StatusApproved:
		return githost.StateSuccess, "Approved by Subspace review", true
	case model.StatusNeedsOwner:
		return githost.StateError, "Needs an owner decision in Subspace", true
	case model.StatusDone:
		return githost.StateSuccess, "Task done in Subspace", true
	}
	return "", "", false
}

func taskKey(t *model.Task) string { return t.Status + "|" + *t.PRURL }

func (s *Service) next() *model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, id := range s.order {
		if s.inflight[id] {
			continue
		}
		s.order = append(s.order[:i], s.order[i+1:]...)
		t := s.pending[id]
		delete(s.pending, id)
		s.inflight[id] = true
		return t
	}
	return nil
}

func (s *Service) finish(id string) {
	s.mu.Lock()
	delete(s.inflight, id)
	more := len(s.order) > 0
	s.mu.Unlock()
	if more {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) work(ctx context.Context) {
	for {
		t := s.next()
		if t == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		s.process(ctx, t)
		s.finish(t.ID)
	}
}

func (s *Service) hostFor(repo, prURL string) *target {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil
	}
	web := strings.ToLower(u.Host)
	if name, ok := s.repos[strings.ToLower(repo)]; ok {
		if h := s.hosts[name]; h != nil && h.web == web {
			return h
		}
		return nil
	}
	return s.byWeb[web]
}

func (s *Service) process(ctx context.Context, t *model.Task) {
	state, desc, _ := Map(t.Status)
	repo, number, err := githost.ParsePRURL(*t.PRURL)
	if err != nil || !strings.EqualFold(repo, t.Repo) {
		return
	}
	h := s.hostFor(repo, *t.PRURL)
	if h == nil {
		return
	}
	key := taskKey(t)
	var sha string
	err = s.call(ctx, h, func() (err error) {
		sha, err = h.client.HeadSHA(ctx, repo, number)
		return err
	})
	if err != nil {
		s.warn(ctx, t, h, "head lookup", err)
		return
	}
	if sha == "" {
		s.warn(ctx, t, h, "head lookup", errors.New("pull request has no head commit"))
		return
	}
	pk := strings.ToLower(repo) + "@" + sha
	s.mu.Lock()
	same := s.posted[pk] == state
	s.mu.Unlock()
	if same {
		s.remember(t.ID, key, pk, state)
		return
	}
	status := githost.CommitStatus{State: state, Context: Context, Description: desc}
	if s.public != "" {
		status.TargetURL = s.public + "/tasks/" + url.PathEscape(t.ID)
	}
	err = s.call(ctx, h, func() error { return h.client.PostStatus(ctx, repo, sha, status) })
	if err != nil {
		s.warn(ctx, t, h, "post status", err)
		return
	}
	s.remember(t.ID, key, pk, state)
}

func (s *Service) remember(taskID, key, pk, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.posted) >= maxPosted {
		clear(s.posted)
	}
	s.posted[pk] = state
	s.sent[taskID] = key
}

func (s *Service) warn(ctx context.Context, t *model.Task, h *target, op string, err error) {
	if ctx.Err() != nil {
		return
	}
	s.log.Warn("forge status failed", "task", t.ID, "host", h.name, "op", op, "err", redact.String(err.Error()))
}

func (s *Service) call(ctx context.Context, h *target, fn func() error) error {
	for attempt := 1; ; attempt++ {
		if err := s.limiter.Wait(ctx, hostKey(h.name)); err != nil {
			return err
		}
		if err := s.limiter.Wait(ctx, globalKey); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		wait, retry := s.retryDelay(err, attempt)
		if !retry || attempt >= s.cfg.Attempts || ctx.Err() != nil {
			return err
		}
		if err := retryq.Sleep(ctx, s.clock, wait); err != nil {
			return err
		}
	}
}

func (s *Service) retryDelay(err error, attempt int) (time.Duration, bool) {
	if errors.Is(err, githost.ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	wait := retryq.Exp(s.cfg.BaseDelay, s.cfg.MaxDelay, attempt)
	var se *githost.StatusError
	if errors.As(err, &se) {
		if !se.Retryable() {
			return 0, false
		}
		wait = max(wait, se.RetryAfter)
		if !se.ResetAt.IsZero() {
			wait = max(wait, se.ResetAt.Sub(s.clock.Now()))
		}
		wait = min(wait, s.cfg.MaxDelay)
	}
	return wait, true
}

type forgejoClient struct{ c *forgejo.Client }

func (f forgejoClient) HeadSHA(ctx context.Context, repo string, number int64) (string, error) {
	pr, err := f.c.GetPR(ctx, repo, number)
	return pr.HeadSHA, err
}

func (f forgejoClient) PostStatus(ctx context.Context, repo, sha string, s githost.CommitStatus) error {
	return f.c.PostStatus(ctx, repo, sha, s)
}

type githubClient struct{ c *github.Client }

func (g githubClient) HeadSHA(ctx context.Context, repo string, number int64) (string, error) {
	pr, err := g.c.GetPR(ctx, github.PRRef{Repo: repo, Number: int(number)})
	if err != nil {
		return "", err
	}
	return pr.HeadSHA, nil
}

func (g githubClient) PostStatus(ctx context.Context, repo, sha string, s githost.CommitStatus) error {
	return g.c.PostStatus(ctx, repo, sha, s)
}

func newClient(hc prsync.HostConfig, httpClient *http.Client) (Client, error) {
	switch hc.Kind {
	case prsync.KindForgejo:
		base, ok := strings.CutSuffix(hc.APIURL, "/api/v1")
		if !ok {
			return nil, errors.New("api_url must end with /api/v1")
		}
		return forgejoClient{forgejo.New(base, hc.Token, httpClient)}, nil
	case prsync.KindGitHub:
		return githubClient{github.New(github.Config{APIURL: hc.APIURL, Token: hc.Token, HTTPClient: httpClient})}, nil
	}
	return nil, fmt.Errorf("unknown kind %q", hc.Kind)
}
