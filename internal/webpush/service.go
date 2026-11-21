package webpush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/webhookout"
)

const (
	DefaultTTL     = 12 * time.Hour
	DefaultSubject = "mailto:webpush@subspace.invalid"
	MaxSubs        = 20
	MaxFailures    = 10
	maxName        = 100
	maxTitleRunes  = 80
	maxBodyRunes   = 240
	padBlock       = 256
	requestTimeout = 15 * time.Second
	maxTracked     = 256
	maxRetryAfter  = time.Hour
)

type Subscription struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Host          string     `json:"host"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Failures      int        `json:"failures"`

	Endpoint string `json:"-"`
	p256dh   string
	auth     string
}

type InputKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type Input struct {
	Endpoint       string    `json:"endpoint"`
	ExpirationTime *float64  `json:"expirationTime"`
	Keys           InputKeys `json:"keys"`
	Name           string    `json:"name"`
}

type Remove struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

type Key struct {
	PublicKey string `json:"public_key"`
}

type Options struct {
	Store   store.Store
	Hosts   *Hosts
	Guard   *webhookout.Guard
	Client  *http.Client
	Subject string
	TTL     time.Duration
	Rate    ratelimit.Limit
	Clock   ratelimit.Clock
	Log     *slog.Logger

	OnSubscribe func()
}

type Service struct {
	o     Options
	db    db
	vapid *VAPID
	rate  *ratelimit.Limiter

	mu    sync.Mutex
	sent  map[string]map[string]bool
	until map[string]time.Time

	limited atomic.Int64
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

func New(ctx context.Context, o Options) (*Service, error) {
	if o.Clock == nil {
		o.Clock = systemClock{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Subject == "" {
		o.Subject = DefaultSubject
	}
	if o.Hosts == nil {
		o.Hosts, _ = NewHosts("")
	}
	if o.Guard == nil {
		o.Guard = webhookout.NewGuard(nil)
	}
	if o.Client == nil {
		o.Client = o.Guard.Client(requestTimeout)
	}
	v, err := loadVAPID(ctx, o.Store)
	if err != nil {
		return nil, fmt.Errorf("webpush: vapid key: %w", err)
	}
	return &Service{
		o: o, db: db{o.Store}, vapid: v,
		rate:  ratelimit.New(ratelimit.Options{Default: o.Rate, Clock: o.Clock}),
		sent:  map[string]map[string]bool{},
		until: map[string]time.Time{},
	}, nil
}

func (s *Service) now() time.Time { return s.o.Clock.Now().UTC().Truncate(time.Millisecond) }

func (s *Service) PublicKey() string { return s.vapid.PublicKey() }

func (s *Service) Limited() int64 { return s.limited.Load() }

func (s *Service) List(ctx context.Context) ([]*Subscription, error) {
	subs, err := s.db.list(ctx)
	for _, sub := range subs {
		sub.Host = hostOf(sub.Endpoint)
	}
	return subs, err
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (s *Service) Subscribe(ctx context.Context, in Input) (*Subscription, bool, error) {
	u, err := s.o.Hosts.Check(strings.TrimSpace(in.Endpoint), s.o.Guard.Blocked)
	if err != nil {
		return nil, false, model.BadRequest("%v", err)
	}
	if _, err := ParseKeys(in.Keys.P256DH, in.Keys.Auth); err != nil {
		return nil, false, model.BadRequest("%v", err)
	}
	name := strings.Join(strings.Fields(in.Name), " ")
	if len([]rune(name)) > maxName || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return nil, false, model.BadRequest("name must be at most %d characters", maxName)
	}
	sub := &Subscription{
		Name: name, Host: u.Hostname(), CreatedAt: s.now(), Endpoint: u.String(),
		p256dh: strings.TrimRight(in.Keys.P256DH, "="), auth: strings.TrimRight(in.Keys.Auth, "="),
	}
	if in.ExpirationTime != nil {
		v := *in.ExpirationTime
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e15 {
			return nil, false, model.BadRequest("expirationTime must be a Unix time in milliseconds")
		}
		t := time.UnixMilli(int64(v)).UTC()
		if !t.After(sub.CreatedAt) {
			return nil, false, model.BadRequest("subscription has already expired")
		}
		sub.ExpiresAt = &t
	}
	created, err := s.db.upsert(ctx, sub, MaxSubs, func(created bool) *model.AuditEvent {
		return s.audit(ctx, "create_push_subscription", nil, map[string]any{"id": sub.ID, "host": sub.Host, "name": sub.Name, "created": created})
	})
	if err != nil {
		return nil, false, err
	}
	if s.o.OnSubscribe != nil {
		s.o.OnSubscribe()
	}
	return sub, created, nil
}

func (s *Service) Unsubscribe(ctx context.Context, in Remove) error {
	where, arg := "id", strings.TrimSpace(in.ID)
	switch {
	case arg != "" && in.Endpoint != "":
		return model.BadRequest("give either id or endpoint")
	case arg == "":
		where, arg = "endpoint", strings.TrimSpace(in.Endpoint)
		if arg == "" {
			return model.BadRequest("id or endpoint is required")
		}
		if u, err := url.Parse(arg); err == nil && u.Scheme == "https" {
			u.Host = strings.ToLower(u.Host)
			arg = u.String()
		}
	}
	_, err := s.db.delete(ctx, where, arg, func(sub *Subscription) *model.AuditEvent {
		return s.audit(ctx, "delete_push_subscription", map[string]any{"id": sub.ID, "host": hostOf(sub.Endpoint), "name": sub.Name}, nil)
	})
	return err
}

func (s *Service) audit(ctx context.Context, tool string, before, after map[string]any) *model.AuditEvent {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool, Before: before, After: after}
	if note := auth.AuditNote(ctx, ""); note != "" {
		e.Note = &note
	}
	return e
}

func (s *Service) drop(ctx context.Context, sub *Subscription, reason string) {
	_, err := s.db.delete(ctx, "id", sub.ID, func(sub *Subscription) *model.AuditEvent {
		note := reason
		return &model.AuditEvent{TS: s.now(), AgentID: "system", Role: "system", Tool: "expire_push_subscription",
			Before: map[string]any{"id": sub.ID, "host": hostOf(sub.Endpoint), "name": sub.Name}, Note: &note}
	})
	if err != nil && model.AsError(err).Code != "not_found" {
		s.o.Log.Error("webpush: remove subscription", "id", sub.ID, "err", err)
		return
	}
	s.mu.Lock()
	delete(s.until, sub.ID)
	s.mu.Unlock()
	s.o.Log.Info("webpush: subscription removed", "id", sub.ID, "reason", reason)
}

type Message struct {
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Tag     string    `json:"tag"`
	URL     string    `json:"url"`
	TS      time.Time `json:"ts"`
	Actions []Action  `json:"actions,omitempty"`
}

type Action struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

func clip(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func link(n notify.Notification) string {
	switch {
	case n.TaskID != "":
		return "/tasks/" + url.PathEscape(n.TaskID)
	case n.Kind == "question":
		return "/questions"
	case n.Kind == "plan":
		return "/plans"
	}
	return "/"
}

func encode(n notify.Notification) ([]byte, error) {
	m := Message{Title: clip(n.Title, maxTitleRunes), Body: clip(n.Text, maxBodyRunes), Tag: n.ID, URL: link(n), TS: n.TS.UTC()}
	for i, a := range n.Actions {
		m.Actions = append(m.Actions, Action{Action: "a" + strconv.Itoa(i), Title: clip(a.Label, 40), URL: a.URL})
	}
	for {
		b, err := json.Marshal(m)
		if err != nil || len(b) <= MaxPayload {
			return b, err
		}
		if len(m.Actions) == 0 {
			m.Body = ""
			return json.Marshal(m)
		}
		m.Actions = m.Actions[:len(m.Actions)-1]
	}
}

func padding(n int) int {
	return min((padBlock-(n+1)%padBlock)%padBlock, MaxPayload-n)
}

func urgency(priority int) string {
	switch {
	case priority >= 4:
		return "high"
	case priority == 3:
		return "normal"
	}
	return "low"
}

func topic(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return hex.EncodeToString(sum[:16])
}

func (s *Service) Name() string { return "webpush" }

type outcome int

const (
	delivered outcome = iota
	retry
	failed
	gone
)

var errBackoff = errors.New("webpush: push service asked to back off")

func (s *Service) Send(ctx context.Context, n notify.Notification) error {
	subs, err := s.db.list(ctx)
	if err != nil || len(subs) == 0 {
		return err
	}
	body, err := encode(n)
	if err != nil {
		return err
	}
	key := n.ID + "|" + n.TS.UTC().Format(time.RFC3339Nano)
	var transient error
	for _, sub := range subs {
		now := s.now()
		s.mu.Lock()
		done := s.sent[key][sub.ID]
		wait := s.until[sub.ID]
		s.mu.Unlock()
		switch {
		case done:
			continue
		case sub.ExpiresAt != nil && !now.Before(*sub.ExpiresAt):
			s.drop(ctx, sub, "expired")
			continue
		case now.Before(wait):
			transient = errBackoff
			continue
		case !s.rate.Allow(sub.ID):
			s.limited.Add(1)
			s.mark(key, sub.ID)
			continue
		}
		res, err := s.push(ctx, sub, body, n)
		switch res {
		case delivered:
			s.mark(key, sub.ID)
			s.record(ctx, sub, true)
		case gone:
			s.drop(ctx, sub, "gone")
		case retry:
			transient = err
			s.record(ctx, sub, false)
		case failed:
			s.mark(key, sub.ID)
			s.o.Log.Warn("webpush: delivery rejected", "id", sub.ID, "err", err)
			s.record(ctx, sub, false)
		}
	}
	return transient
}

func (s *Service) mark(key, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.sent[key]
	if m == nil {
		if len(s.sent) >= maxTracked {
			clear(s.sent)
		}
		m = map[string]bool{}
		s.sent[key] = m
	}
	m[id] = true
}

func (s *Service) record(ctx context.Context, sub *Subscription, ok bool) {
	if !ok && sub.Failures+1 >= MaxFailures {
		s.drop(ctx, sub, "failing")
		return
	}
	if err := s.db.result(ctx, sub.ID, ok, s.now()); err != nil {
		s.o.Log.Error("webpush: record result", "id", sub.ID, "err", err)
	}
}

func (s *Service) push(ctx context.Context, sub *Subscription, body []byte, n notify.Notification) (outcome, error) {
	u, err := s.o.Hosts.Check(sub.Endpoint, s.o.Guard.Blocked)
	if err != nil {
		return gone, err
	}
	keys, err := ParseKeys(sub.p256dh, sub.auth)
	if err != nil {
		return gone, err
	}
	payload, err := Encrypt(body, keys, padding(len(body)))
	if err != nil {
		return failed, err
	}
	authz, err := s.vapid.Authorization(origin(u), s.o.Subject, s.o.Clock.Now())
	if err != nil {
		return failed, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return failed, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.FormatInt(int64(s.o.TTL/time.Second), 10))
	req.Header.Set("Urgency", urgency(n.Priority))
	req.Header.Set("Topic", topic(n.ID))
	req.Header.Set("Authorization", authz)
	resp, err := s.o.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, webhookout.ErrBlocked) {
			return gone, err
		}
		return retry, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	err = fmt.Errorf("push service answered %d", resp.StatusCode)
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return delivered, nil
	case c == http.StatusNotFound || c == http.StatusGone:
		return gone, err
	case c == http.StatusTooManyRequests || c == http.StatusServiceUnavailable:
		s.backoff(sub.ID, resp.Header.Get("Retry-After"))
		return retry, err
	case c >= 500 || c == http.StatusRequestTimeout:
		return retry, err
	}
	return failed, err
}

func (s *Service) backoff(id, header string) {
	now := s.o.Clock.Now()
	d := time.Duration(0)
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(header); err == nil {
		d = t.Sub(now)
	}
	if d <= 0 {
		return
	}
	s.mu.Lock()
	s.until[id] = now.UTC().Truncate(time.Millisecond).Add(min(d, maxRetryAfter))
	s.mu.Unlock()
}
