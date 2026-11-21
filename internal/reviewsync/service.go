package reviewsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/retryq"
)

const (
	reviewPrefix  = "Review ("
	maxRetryAfter = 10 * time.Minute
)

type Mode int

const (
	ModeChangesRequested Mode = iota + 1
	ModeAll
)

func ParseMode(s string) (Mode, bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return 0, false, nil
	case VerdictChangesRequested:
		return ModeChangesRequested, true, nil
	case "all":
		return ModeAll, true, nil
	}
	return 0, false, fmt.Errorf("reviewsync: unknown mode %q (want off, changes_requested or all)", s)
}

type Tasks interface {
	GetTask(ctx context.Context, a model.Actor, id string) (*model.Task, error)
}

type Record struct {
	PRURL     string
	CommentID int64
	MessageID int64
	Digest    string
}

type Store interface {
	ReviewComment(ctx context.Context, taskID string) (Record, bool, error)
	SetReviewComment(ctx context.Context, taskID string, r Record) error
}

type Host struct {
	Web       string
	Commenter githost.Commenter
}

type Options struct {
	Tasks     Tasks
	Store     Store
	Hosts     map[string]Host
	Repos     map[string]string
	PublicURL string
	Mode      Mode
	Clock     ratelimit.Clock
	Log       *slog.Logger
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

type job struct {
	seq        int64
	taskID     string
	msgID      int64
	verdict    string
	comments   string
	reviewer   string
	updateOnly bool
	attempts   int
	due        time.Time
	posted     *Record
}

type Service struct {
	o       Options
	baseURL string
	clock   ratelimit.Clock
	log     *slog.Logger
	byWeb   map[string]Host
	wake    chan struct{}
	wg      sync.WaitGroup

	mu   sync.Mutex
	seq  int64
	jobs map[string]*job
}

func New(o Options) (*Service, error) {
	if o.Tasks == nil || o.Store == nil || len(o.Hosts) == 0 {
		return nil, errors.New("reviewsync: tasks, store and hosts are required")
	}
	if o.Mode != ModeChangesRequested && o.Mode != ModeAll {
		return nil, errors.New("reviewsync: invalid mode")
	}
	if o.Clock == nil {
		o.Clock = retryq.SystemClock{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Attempts <= 0 {
		o.Attempts = 6
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = time.Minute
	}
	s := &Service{
		o: o, clock: o.Clock, log: o.Log, baseURL: publicBase(o.PublicURL),
		byWeb: map[string]Host{}, wake: make(chan struct{}, 1), jobs: map[string]*job{},
	}
	for _, name := range slices.Sorted(maps.Keys(o.Hosts)) {
		h := o.Hosts[name]
		if _, dup := s.byWeb[h.Web]; !dup {
			s.byWeb[h.Web] = h
		}
	}
	return s, nil
}

func publicBase(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		strings.ContainsAny(u.String(), " ()<>\"`\\") {
		return ""
	}
	return u.String()
}

func (s *Service) Start(ctx context.Context, hub events.Hub) {
	sub := hub.Subscribe([]string{events.TopicAll})
	s.wg.Go(func() {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.C():
				if ev.Type != "message.created" {
					continue
				}
				switch m := ev.Data.(type) {
				case *model.Message:
					if m != nil {
						s.Enqueue(*m)
					}
				case model.Message:
					s.Enqueue(m)
				}
			}
		}
	})
	s.wg.Go(func() { s.Run(ctx) })
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) Enqueue(m model.Message) bool {
	if m.ThreadKind != model.ThreadTask || m.AuthorRole != model.RoleReviewer || m.Kind != model.MsgNote ||
		m.ID <= 0 || !taskIDRe.MatchString(m.ThreadID) {
		return false
	}
	rest, ok := strings.CutPrefix(m.Body, reviewPrefix)
	if !ok {
		return false
	}
	verdict, comments, ok := strings.Cut(rest, "): ")
	if !ok && strings.HasSuffix(rest, "):") {
		verdict, comments, ok = strings.TrimSuffix(rest, "):"), "", true
	}
	if !ok || (verdict != VerdictApprove && verdict != VerdictChangesRequested) {
		return false
	}
	updateOnly := verdict == VerdictApprove && s.o.Mode != ModeAll
	s.mu.Lock()
	s.seq++
	s.jobs[m.ThreadID] = &job{
		seq: s.seq, taskID: m.ThreadID, msgID: m.ID, verdict: verdict, comments: comments,
		reviewer: m.AuthorID, updateOnly: updateOnly, due: s.clock.Now(),
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

func (s *Service) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

func (s *Service) Run(ctx context.Context) {
	for {
		next, ok := s.Process(ctx)
		if ctx.Err() != nil {
			return
		}
		var timer <-chan time.Time
		stop := func() bool { return false }
		if ok {
			timer, stop = s.clock.Timer(max(next.Sub(s.clock.Now()), 0))
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-s.wake:
			stop()
		case <-timer:
		}
	}
}

func (s *Service) Process(ctx context.Context) (time.Time, bool) {
	now := s.clock.Now()
	s.mu.Lock()
	var due []*job
	for _, j := range s.jobs {
		if !j.due.After(now) {
			due = append(due, j)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(due, func(a, b *job) int { return int(a.seq - b.seq) })
	for _, j := range due {
		if ctx.Err() != nil {
			break
		}
		s.finish(ctx, j, s.sync(ctx, j), now)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, j := range s.jobs {
		if next.IsZero() || j.due.Before(next) {
			next = j.due
		}
	}
	return next, !next.IsZero()
}

func (s *Service) finish(ctx context.Context, j *job, err error, now time.Time) {
	if err != nil && ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs[j.taskID] != j {
		return
	}
	if err == nil {
		delete(s.jobs, j.taskID)
		return
	}
	j.attempts++
	var se *githost.StatusError
	switch {
	case errors.Is(err, githost.ErrNotFound), errors.As(err, &se) && !se.Retryable(), j.attempts >= s.o.Attempts:
		delete(s.jobs, j.taskID)
		s.log.Warn("review comment not posted", "task", j.taskID, "attempts", j.attempts, "err", err)
		return
	}
	d := retryq.Exp(s.o.BaseDelay, s.o.MaxDelay, j.attempts)
	if se != nil {
		d = max(d, min(se.RetryAfter, maxRetryAfter))
	}
	j.due = now.Add(d)
	s.log.Warn("review comment failed, will retry", "task", j.taskID, "attempt", j.attempts, "retry_in", d, "err", err)
}

func (s *Service) commenter(repo, prURL string) githost.Commenter {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil
	}
	web := strings.ToLower(u.Host)
	if name, ok := s.o.Repos[strings.ToLower(repo)]; ok {
		if h, ok := s.o.Hosts[name]; ok && h.Web == web {
			return h.Commenter
		}
		return nil
	}
	return s.byWeb[web].Commenter
}

func (s *Service) sync(ctx context.Context, j *job) error {
	if j.posted != nil {
		if err := s.o.Store.SetReviewComment(ctx, j.taskID, *j.posted); err != nil {
			return err
		}
		j.posted = nil
		return nil
	}
	t, err := s.o.Tasks.GetTask(ctx, model.Owner, j.taskID)
	if err != nil {
		if model.AsError(err).Code == "not_found" {
			return nil
		}
		return err
	}
	if t.PRURL == nil || *t.PRURL == "" {
		return nil
	}
	prURL := *t.PRURL
	repo, number, err := githost.ParsePRURL(prURL)
	if err != nil || !strings.EqualFold(repo, t.Repo) {
		return nil
	}
	c := s.commenter(repo, prURL)
	if c == nil {
		s.log.Warn("review comment skipped: no git host configured", "task", t.ID, "repo", repo)
		return nil
	}
	rec, ok, err := s.o.Store.ReviewComment(ctx, t.ID)
	if err != nil {
		return err
	}
	if ok && rec.PRURL != prURL {
		rec, ok = Record{}, false
	}
	if ok && rec.MessageID >= j.msgID {
		return nil
	}
	if !ok && j.updateOnly {
		return nil
	}
	body := Render(Review{
		TaskID: t.ID, Title: t.Title, Criteria: t.AcceptanceCriteria, Round: t.ReviewRounds,
		NeedsOwner: t.Status == model.StatusNeedsOwner && j.verdict == VerdictChangesRequested,
		Verdict:    j.verdict, Reviewer: j.reviewer, Comments: j.comments, TaskURL: s.taskURL(t.ID),
	})
	next := Record{PRURL: prURL, CommentID: rec.CommentID, MessageID: j.msgID, Digest: Digest(body)}
	if !ok || rec.Digest != next.Digest {
		if ok {
			err = c.UpdateComment(ctx, repo, rec.CommentID, body)
			if errors.Is(err, githost.ErrNotFound) {
				ok = false
			} else if err != nil {
				return err
			}
		}
		if !ok {
			id, err := c.CreateComment(ctx, repo, number, body)
			if err != nil {
				return err
			}
			next.CommentID = id
		}
	}
	j.posted = &next
	if err := s.o.Store.SetReviewComment(ctx, t.ID, next); err != nil {
		return err
	}
	j.posted = nil
	return nil
}

func (s *Service) taskURL(id string) string {
	if s.baseURL == "" {
		return ""
	}
	return s.baseURL + "/tasks/" + id
}
