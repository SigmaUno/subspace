package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/redact"
)

const rateKey = "mirror"

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

type Link struct {
	Repo   string
	URL    string
	Digest string
}

type Links interface {
	Get(ctx context.Context, taskID string) (Link, bool, error)
	Put(ctx context.Context, taskID string, l Link) error
}

type SyncConfig struct {
	Mirrors    map[string]IssueMirror
	Links      Links
	BriefTitle func(ctx context.Context, briefID string) string
	Clock      ratelimit.Clock
	RatePerMin float64
	Burst      int
	Attempts   int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	MaxQueued  int
	Log        *slog.Logger
}

type Syncer struct {
	cfg     SyncConfig
	limiter *ratelimit.Limiter
	wg      sync.WaitGroup
	wake    chan struct{}

	mu      sync.Mutex
	pending map[string]*model.Task
	order   []string
}

func NewSyncer(cfg SyncConfig) *Syncer {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.RatePerMin <= 0 {
		cfg.RatePerMin = 60
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 5
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 5
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = time.Second
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}
	if cfg.MaxQueued <= 0 {
		cfg.MaxQueued = 4096
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Syncer{
		cfg:     cfg,
		limiter: ratelimit.New(ratelimit.Options{Default: ratelimit.Limit{PerMinute: cfg.RatePerMin, Burst: cfg.Burst}, Clock: cfg.Clock}),
		wake:    make(chan struct{}, 1),
		pending: map[string]*model.Task{},
	}
}

func (s *Syncer) Start(ctx context.Context, hub events.Hub) {
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
	s.wg.Go(func() { s.work(ctx) })
}

func (s *Syncer) Wait() { s.wg.Wait() }

func (s *Syncer) Enqueue(data any) {
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
	if _, ok := s.cfg.Mirrors[t.Repo]; !ok || t.ID == "" {
		return
	}
	s.mu.Lock()
	if _, queued := s.pending[t.ID]; !queued {
		if len(s.order) >= s.cfg.MaxQueued {
			delete(s.pending, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, t.ID)
	}
	s.pending[t.ID] = &t
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Syncer) next() *model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	id := s.order[0]
	s.order = s.order[1:]
	t := s.pending[id]
	delete(s.pending, id)
	return t
}

func (s *Syncer) superseded(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[id]
	return ok
}

func (s *Syncer) work(ctx context.Context) {
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
		s.sync(ctx, t)
	}
}

func (s *Syncer) sync(ctx context.Context, t *model.Task) {
	view := s.view(ctx, t)
	digest := fingerprint(view)
	link, ok, err := s.cfg.Links.Get(ctx, t.ID)
	if err != nil {
		s.cfg.Log.Warn("mirror link lookup failed", "task", t.ID, "err", redact.String(err.Error()))
	}
	if ok && link.Repo == t.Repo && link.Digest == digest {
		return
	}
	m := s.cfg.Mirrors[t.Repo]
	var issueURL string
	for attempt := 1; ; attempt++ {
		if err := s.limiter.Wait(ctx, rateKey); err != nil {
			return
		}
		issueURL, err = m.Upsert(ctx, view)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		if attempt >= s.cfg.Attempts {
			s.cfg.Log.Warn("mirror upsert failed", "task", t.ID, "attempts", attempt, "err", redact.String(err.Error()))
			return
		}
		timer, stop := s.cfg.Clock.Timer(s.backoff(attempt))
		select {
		case <-timer:
		case <-ctx.Done():
			stop()
			return
		}
		if s.superseded(t.ID) {
			return
		}
	}
	if err := s.cfg.Links.Put(ctx, t.ID, Link{Repo: t.Repo, URL: issueURL, Digest: digest}); err != nil {
		s.cfg.Log.Warn("mirror link save failed", "task", t.ID, "err", redact.String(err.Error()))
	}
}

func (s *Syncer) backoff(attempt int) time.Duration {
	d := s.cfg.MaxDelay
	if attempt < 32 && s.cfg.BaseDelay<<(attempt-1) < d {
		d = s.cfg.BaseDelay << (attempt - 1)
	}
	return d
}

func (s *Syncer) view(ctx context.Context, t *model.Task) TaskView {
	v := TaskView{ID: t.ID, Title: redact.String(t.Title), Status: t.Status}
	if t.Assignee != nil {
		v.Assignee = *t.Assignee
	}
	if t.PRURL != nil {
		v.PRURL = stripUserinfo(*t.PRURL)
	}
	if s.cfg.BriefTitle != nil && t.BriefID != "" {
		v.BriefTitle = redact.String(s.cfg.BriefTitle(ctx, t.BriefID))
	}
	for _, a := range t.AcceptanceCriteria {
		v.Acceptance = append(v.Acceptance, redact.String(a))
	}
	return v
}

func stripUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func fingerprint(v TaskView) string {
	sum := sha256.Sum256([]byte(v.Title + "\n" + RenderBody(v)))
	return hex.EncodeToString(sum[:])
}
