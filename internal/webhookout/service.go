package webhookout

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/retryq"
	"github.com/SigmaUno/subspace/internal/trace"
)

const (
	SecretPrefix = "whsec_"

	BaseDelay      = 10 * time.Second
	MaxDelay       = 4 * time.Hour
	MaxAge         = 24 * time.Hour
	MaxSubs        = 100
	DefaultPending = 1000
	KeepHistory    = 500
	maxEvents      = 20
	requestTimeout = 15 * time.Second
	pollEvery      = 500 * time.Millisecond
	batchSize      = 256
)

var briefID = regexp.MustCompile(`^B-[0-9]+$`)

type Options struct {
	Store  Store
	Hub    events.Hub
	Now    func() time.Time
	Guard  *Guard
	Client *http.Client
	Log    *slog.Logger
	Jitter func(time.Duration) time.Duration
	Poll   time.Duration

	MaxPending int
}

type Service struct {
	o        Options
	mu       sync.Mutex
	ctx      context.Context
	stop     chan struct{}
	subs     []*Subscription
	inflight map[string]bool
	wake     chan struct{}
}

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Jitter == nil {
		o.Jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			n, err := crand.Int(crand.Reader, big.NewInt(int64(max)))
			if err != nil {
				return 0
			}
			return time.Duration(n.Int64())
		}
	}
	if o.MaxPending <= 0 {
		o.MaxPending = DefaultPending
	}
	if o.Poll <= 0 {
		o.Poll = pollEvery
	}
	if o.Client == nil {
		o.Client = o.Guard.Client(requestTimeout)
	}
	return &Service{o: o, inflight: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (s *Service) now() time.Time { return s.o.Now().UTC().Truncate(time.Millisecond) }

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	if err := s.refresh(ctx); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(s.o.Poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.wake:
			}
			s.dispatch(ctx)
		}
	}()
	return nil
}

func (s *Service) refresh(ctx context.Context) error {
	subs, err := s.o.Store.ListSubs(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = subs
	switch {
	case s.ctx == nil:
	case len(subs) > 0 && s.stop == nil:
		s.stop = make(chan struct{})
		go s.pump(s.ctx, s.o.Hub.Subscribe([]string{events.TopicAll}), s.stop)
	case len(subs) == 0 && s.stop != nil:
		close(s.stop)
		s.stop = nil
	}
	return nil
}

func (s *Service) pump(ctx context.Context, sub events.Subscription, stop <-chan struct{}) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case ev := <-sub.C():
			s.handle(ctx, ev)
		}
	}
}

func (s *Service) handle(ctx context.Context, ev events.Event) {
	s.mu.Lock()
	subs := slices.Clone(s.subs)
	s.mu.Unlock()
	var (
		data    map[string]any
		brief   string
		scanned bool
		queued  bool
	)
	for _, sub := range subs {
		if !matches(sub.Events, ev.Type) {
			continue
		}
		if !scanned {
			data, brief = extract(ev.Data)
			scanned = true
		}
		if sub.BriefID != nil && *sub.BriefID != brief {
			continue
		}
		d := &Delivery{WebhookID: sub.ID, Event: ev.Type, CreatedAt: s.now()}
		err := s.o.Store.Enqueue(ctx, d, func(id string) string { return buildBody(id, ev.Type, d.CreatedAt, data, ev.Trace) }, s.o.MaxPending)
		if err != nil {
			if model.AsError(err).Code != "not_found" && ctx.Err() == nil {
				s.o.Log.Error("webhook enqueue", "webhook", sub.ID, "err", err)
			}
			continue
		}
		queued = queued || d.State == StatePending
	}
	if queued {
		s.poke()
	}
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Tick(ctx context.Context) {
	s.dispatch(ctx).Wait()
}

func (s *Service) dispatch(ctx context.Context) *sync.WaitGroup {
	wg := &sync.WaitGroup{}
	due, err := s.o.Store.Due(ctx, s.now(), batchSize)
	if err != nil {
		if ctx.Err() == nil {
			s.o.Log.Error("webhook due", "err", err)
		}
		return wg
	}
	var order []string
	groups := map[string][]*Due{}
	for _, d := range due {
		if _, ok := groups[d.WebhookID]; !ok {
			order = append(order, d.WebhookID)
		}
		groups[d.WebhookID] = append(groups[d.WebhookID], d)
	}
	for _, id := range order {
		s.mu.Lock()
		busy := s.inflight[id]
		s.inflight[id] = true
		s.mu.Unlock()
		if busy {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.inflight, id)
				s.mu.Unlock()
			}()
			for _, d := range groups[id] {
				if !s.attempt(ctx, d) {
					return
				}
			}
		}()
	}
	return wg
}

func (s *Service) nextAttempt(created, now time.Time, attempts int) (time.Time, bool) {
	deadline := created.Add(MaxAge)
	if !now.Before(deadline) {
		return time.Time{}, false
	}
	half := retryq.Exp(BaseDelay, MaxDelay, attempts) / 2
	at := now.Add(half + s.o.Jitter(half))
	if at.After(deadline) {
		at = deadline
	}
	return at, true
}

func (s *Service) attempt(ctx context.Context, due *Due) bool {
	now := s.now()
	status, err := s.send(ctx, due, now)
	if ctx.Err() != nil {
		return false
	}
	d := &due.Delivery
	d.Attempts++
	d.UpdatedAt = now
	d.LastStatus = nil
	if status != 0 {
		d.LastStatus = &status
	}
	if err == nil {
		d.State, d.NextAttemptAt, d.DeliveredAt, d.LastError = StateDelivered, nil, &now, ""
	} else {
		d.LastError = err.Error()
		if at, ok := s.nextAttempt(d.CreatedAt, now, d.Attempts); ok {
			d.NextAttemptAt = &at
		} else {
			d.State, d.NextAttemptAt = StateDead, nil
		}
	}
	if cerr := s.o.Store.Complete(ctx, d, KeepHistory); cerr != nil && ctx.Err() == nil {
		s.o.Log.Error("webhook complete", "webhook", d.WebhookID, "delivery", d.ID, "err", cerr)
	}
	return err == nil
}

func (s *Service) send(ctx context.Context, due *Due, now time.Time) (int, error) {
	body := []byte(due.Payload)
	traceID, flags := payloadTrace(body)
	ctx, span := trace.Continue(ctx, traceID, flags, "webhook.deliver", trace.KindClient,
		trace.String("subspace.webhook_id", due.WebhookID), trace.String("subspace.event", due.Event),
		trace.String("subspace.delivery_id", due.ID), trace.Int("subspace.attempt", due.Attempts+1))
	defer span.End()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, due.URL, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid url")
	}
	trace.Inject(req.Header, trace.From(ctx))
	ts := now.Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Subspace-Webhooks/1")
	req.Header.Set("X-Subspace-Event", due.Event)
	req.Header.Set("X-Subspace-Delivery", due.ID)
	req.Header.Set("X-Subspace-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Subspace-Signature", Sign(due.Secret, ts, body))
	resp, err := s.o.Client.Do(req)
	if err != nil {
		msg := netguard.Scrub(err).Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		span.Fail("request failed")
		return 0, errors.New(msg)
	}
	defer func() { _ = resp.Body.Close() }()
	netguard.Drain(resp.Body, 64<<10)
	span.SetAttrs(trace.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		span.Fail("receiver answered " + strconv.Itoa(resp.StatusCode))
		return resp.StatusCode, fmt.Errorf("receiver answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

type CreateInput struct {
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	BriefID string   `json:"brief_id"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Subscription, error) {
	u, err := s.o.Guard.CheckURL(strings.TrimSpace(in.URL))
	if err != nil {
		return nil, model.BadRequest("%v", err)
	}
	evs, err := cleanEvents(in.Events)
	if err != nil {
		return nil, err
	}
	sub := &Subscription{URL: u.String(), Events: evs, CreatedAt: s.now()}
	if in.BriefID != "" {
		if !briefID.MatchString(in.BriefID) {
			return nil, model.BadRequest("brief_id must look like B-1")
		}
		sub.BriefID = &in.BriefID
	}
	raw := make([]byte, 32)
	if _, err := crand.Read(raw); err != nil {
		return nil, err
	}
	sub.Secret = SecretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	err = s.o.Store.CreateSub(ctx, sub, MaxSubs, func() *model.AuditEvent {
		return s.event(ctx, "create_webhook_out", nil, map[string]any{"webhook_id": sub.ID, "host": u.Host, "events": sub.Events, "brief_id": in.BriefID})
	})
	if err != nil {
		return nil, err
	}
	return sub, s.refresh(ctx)
}

func cleanEvents(in []string) ([]string, error) {
	var out []string
	for _, e := range in {
		e = strings.TrimSpace(e)
		if !eventPattern.MatchString(e) {
			return nil, model.BadRequest("event filter %q must look like task.updated or decision.*", e)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 || len(out) > maxEvents {
		return nil, model.BadRequest("events needs 1 to %d filters", maxEvents)
	}
	return out, nil
}

func (s *Service) List(ctx context.Context) ([]*Subscription, error) {
	return s.o.Store.ListSubs(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (*Subscription, error) {
	return s.o.Store.GetSub(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	err := s.o.Store.DeleteSub(ctx, id, func() *model.AuditEvent {
		return s.event(ctx, "delete_webhook_out", map[string]any{"webhook_id": id}, nil)
	})
	if err != nil {
		return err
	}
	return s.refresh(ctx)
}

func (s *Service) Deliveries(ctx context.Context, id, state string, limit int) ([]*Delivery, error) {
	switch state {
	case "", StatePending, StateDelivered, StateDead:
	default:
		return nil, model.BadRequest("state must be pending, delivered or dead")
	}
	return s.o.Store.Deliveries(ctx, id, state, limit)
}

func (s *Service) Redeliver(ctx context.Context, id, delivery string) (*Delivery, error) {
	d, err := s.o.Store.Requeue(ctx, id, delivery, s.now(), func() *model.AuditEvent {
		return s.event(ctx, "redeliver_webhook_out", nil, map[string]any{"webhook_id": id, "delivery_id": delivery})
	})
	if err != nil {
		return nil, err
	}
	s.poke()
	return d, nil
}

func (s *Service) event(ctx context.Context, tool string, before, after map[string]any) *model.AuditEvent {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool, Before: before, After: after}
	if note := auth.AuditNote(ctx, ""); note != "" {
		e.Note = &note
	}
	return e
}
