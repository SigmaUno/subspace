package policy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	HoldPending  = "pending"
	HoldApproved = "approved"
	HoldConsumed = "consumed"
	HoldRejected = "rejected"
	HoldExpired  = "expired"

	DefaultSweep     = 30 * time.Second
	defaultDecisions = 100
	maxList          = 1000
)

type Policy struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Document
	Warnings  []Finding `json:"warnings"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Version struct {
	Version   int       `json:"version"`
	Document  Document  `json:"document"`
	CreatedAt time.Time `json:"created_at"`
}

type Hold struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Action    string            `json:"action"`
	ActorID   string            `json:"actor_id"`
	ActorRole string            `json:"actor_role"`
	TaskID    string            `json:"task_id"`
	BriefID   string            `json:"brief_id"`
	PlanID    string            `json:"plan_id"`
	Target    string            `json:"target"`
	Args      map[string]string `json:"args"`
	Message   string            `json:"message"`
	Matched   []Match           `json:"matched"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	DecidedAt *time.Time        `json:"decided_at"`
	Note      string            `json:"note"`
	Result    string            `json:"result"`

	key string
}

type Decision struct {
	ID        string         `json:"id"`
	TS        time.Time      `json:"ts"`
	Action    string         `json:"action"`
	ActorID   string         `json:"actor_id"`
	ActorRole string         `json:"actor_role"`
	TaskID    string         `json:"task_id"`
	BriefID   string         `json:"brief_id"`
	Target    string         `json:"target"`
	Outcome   string         `json:"outcome"`
	Message   string         `json:"message"`
	HoldID    string         `json:"hold_id"`
	Matched   []Match        `json:"matched"`
	Facts     map[string]any `json:"facts"`
	Errors    []string       `json:"errors"`
}

type DecisionFilter struct {
	Outcome string
	Action  string
	TaskID  string
	Before  int64
	Limit   int
}

type Replayer func(ctx context.Context, h *Hold) (string, error)

type Notifier func(ctx context.Context, agentID, body string) error

type Options struct {
	Store  store.Store
	Pub    events.Publisher
	Now    func() time.Time
	Log    *slog.Logger
	Notify Notifier
}

type Service struct {
	st     store.Store
	pub    events.Publisher
	now    func() time.Time
	log    *slog.Logger
	notify Notifier
	cache  cache

	mu      sync.Mutex
	replays map[string]Replayer
}

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{st: o.Store, pub: o.Pub, now: o.Now, log: o.Log, notify: o.Notify, replays: map[string]Replayer{}}
}

func (s *Service) SetReplayer(action string, r Replayer) {
	s.mu.Lock()
	s.replays[action] = r
	s.mu.Unlock()
}

func (s *Service) publish(typ string, data any) {
	if s.pub != nil {
		s.pub.Publish([]string{"control"}, typ, data)
	}
}

func (s *Service) decorate(p *Policy) {
	if p.Rules == nil {
		p.Rules = []Rule{}
	}
	p.Warnings = Lint(p.Document)
}

func (s *Service) validate(d *Document) error {
	d.normalize()
	fs := Lint(*d)
	if !HasErrors(fs) {
		return nil
	}
	var msgs []string
	for _, f := range fs {
		if f.Severity == SevError {
			m := f.Message
			if f.Rule != "" {
				m = f.Rule + ": " + m
			}
			msgs = append(msgs, m)
		}
	}
	return &model.Error{Code: "bad_request", Message: "invalid policy: " + strings.Join(msgs, "; "), Details: map[string]any{"findings": fs}}
}

func (s *Service) List(ctx context.Context) ([]*Policy, error) {
	var out []*Policy
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		out, err = listPolicies(q)
		return err
	}))
	for _, p := range out {
		s.decorate(p)
	}
	return out, err
}

func (s *Service) Get(ctx context.Context, id string) (*Policy, error) {
	var out *Policy
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		out, err = getPolicy(q, id)
		return err
	}))
	if err != nil {
		return nil, err
	}
	s.decorate(out)
	return out, nil
}

func (s *Service) Versions(ctx context.Context, id string) ([]Version, error) {
	var out []Version
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		if _, err := getPolicy(q, id); err != nil {
			return err
		}
		var err error
		out, err = listVersions(q, id)
		return err
	}))
	return out, err
}

func (s *Service) Create(ctx context.Context, d Document) (*Policy, error) {
	if err := s.validate(&d); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	p := &Policy{Version: 1, Document: d, CreatedAt: now, UpdatedAt: now}
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		n, err := countPolicies(q)
		if err != nil {
			return err
		}
		if n >= MaxPolicies {
			return model.Conflict("at most %d policies are allowed", MaxPolicies)
		}
		if err := insertPolicy(t, q, p); err != nil {
			return err
		}
		if err := bumpRev(t); err != nil {
			return err
		}
		return s.audit(ctx, t, "create_policy", nil, p)
	}))
	s.cache.invalidate()
	if err != nil {
		return nil, err
	}
	s.decorate(p)
	return p, nil
}

func (s *Service) Replace(ctx context.Context, id string, d Document) (*Policy, error) {
	if err := s.validate(&d); err != nil {
		return nil, err
	}
	var out *Policy
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		cur, err := getPolicy(q, id)
		if err != nil {
			return err
		}
		next := *cur
		next.Document, next.Version, next.UpdatedAt = d, cur.Version+1, s.now().UTC()
		if err := updatePolicy(q, &next); err != nil {
			return err
		}
		if err := bumpRev(t); err != nil {
			return err
		}
		out = &next
		return s.audit(ctx, t, "update_policy", cur, &next)
	}))
	s.cache.invalidate()
	if err != nil {
		return nil, err
	}
	s.decorate(out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		cur, err := getPolicy(q, id)
		if err != nil {
			return err
		}
		if err := deletePolicy(q, id); err != nil {
			return err
		}
		if err := bumpRev(t); err != nil {
			return err
		}
		return s.audit(ctx, t, "delete_policy", cur, nil)
	}))
	s.cache.invalidate()
	return err
}

func (s *Service) audit(ctx context.Context, t store.Tx, tool string, before, after *Policy) error {
	ref := after
	if ref == nil {
		ref = before
	}
	note := auth.AuditNote(ctx, ref.ID)
	e := &model.AuditEvent{TS: s.now().UTC(), AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool, Note: &note}
	if before != nil {
		e.Before = summary(before)
	}
	if after != nil {
		e.After = summary(after)
	}
	return t.InsertAudit(e)
}

func summary(p *Policy) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "version": p.Version, "enabled": p.Enabled, "rules": len(p.Rules)}
}

func (s *Service) Decisions(ctx context.Context, f DecisionFilter) ([]*Decision, error) {
	if f.Limit <= 0 {
		f.Limit = defaultDecisions
	}
	f.Limit = min(f.Limit, maxList)
	var out []*Decision
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		out, err = listDecisions(q, f)
		return err
	}))
	for _, d := range out {
		if d.Matched == nil {
			d.Matched = []Match{}
		}
		if d.Errors == nil {
			d.Errors = []string{}
		}
		if d.Facts == nil {
			d.Facts = map[string]any{}
		}
	}
	return out, err
}

func (s *Service) Holds(ctx context.Context, status string, limit int) ([]*Hold, error) {
	if limit <= 0 {
		limit = defaultDecisions
	}
	limit = min(limit, maxList)
	var out []*Hold
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		if status == "" {
			out, err = queryHolds(q, `ORDER BY seq DESC LIMIT ?`, limit)
		} else {
			out, err = queryHolds(q, `WHERE status=? ORDER BY seq DESC LIMIT ?`, status, limit)
		}
		return err
	}))
	for _, h := range out {
		s.view(h)
	}
	return out, err
}

func (s *Service) view(h *Hold) {
	if h.Status == HoldPending && !s.now().Before(h.ExpiresAt) {
		h.Status = HoldExpired
	}
	if h.Matched == nil {
		h.Matched = []Match{}
	}
	if h.Args == nil {
		h.Args = map[string]string{}
	}
}

func (s *Service) GetHold(ctx context.Context, id string) (*Hold, error) {
	var out *Hold
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		out, err = getHold(q, id)
		return err
	}))
	if err != nil {
		return nil, err
	}
	s.view(out)
	return out, nil
}

func (s *Service) Approve(ctx context.Context, id, note string) (*Hold, error) {
	h, err := s.decide(ctx, id, HoldApproved, "approve_policy_hold", note)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	replay := s.replays[h.Action]
	s.mu.Unlock()
	msg := fmt.Sprintf("Owner approved %s (%s%s); retry the action now.", h.ID, h.Action, onRef(h))
	if replay != nil {
		result, rerr := replay(context.WithoutCancel(ctx), h)
		if rerr != nil {
			result = "replay failed: " + rerr.Error()
		}
		if err := s.setResult(ctx, h.ID, result); err != nil {
			return nil, err
		}
		if rerr == nil {
			msg = fmt.Sprintf("Owner approved %s (%s%s); it was carried out: %s", h.ID, h.Action, onRef(h), result)
		}
	}
	s.tell(ctx, h, msg)
	return s.GetHold(ctx, id)
}

func (s *Service) Reject(ctx context.Context, id, note string) (*Hold, error) {
	h, err := s.decide(ctx, id, HoldRejected, "reject_policy_hold", note)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Owner rejected %s (%s%s)", h.ID, h.Action, onRef(h))
	if note != "" {
		msg += ": " + note
	}
	s.tell(ctx, h, msg+". Do not retry; use ask_owner if unsure.")
	return h, nil
}

func onRef(h *Hold) string {
	ref := h.TaskID
	if ref == "" {
		ref = h.PlanID
	}
	if ref == "" {
		ref = h.BriefID
	}
	if ref == "" {
		return ""
	}
	return " on " + ref
}

func (s *Service) tell(ctx context.Context, h *Hold, body string) {
	if s.notify == nil || h.ActorRole == model.RoleOwner || h.ActorID == "" {
		return
	}
	if err := s.notify(context.WithoutCancel(ctx), h.ActorID, body); err != nil {
		s.log.Warn("notify policy hold", "hold", h.ID, "err", err)
	}
}

func (s *Service) decide(ctx context.Context, id, status, tool, note string) (*Hold, error) {
	var out *Hold
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		h, err := getHold(q, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if h.Status == HoldPending && !now.Before(h.ExpiresAt) {
			return model.Conflict("hold %s expired at %s", id, h.ExpiresAt.Format(time.RFC3339))
		}
		if h.Status != HoldPending {
			return model.Conflict("hold %s is %s, not pending", id, h.Status)
		}
		h.Status, h.DecidedAt, h.Note = status, &now, note
		if err := setHold(q, h); err != nil {
			return err
		}
		out = h
		an := auth.AuditNote(ctx, note)
		return t.InsertAudit(&model.AuditEvent{TS: now, AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool,
			TaskID: optString(h.TaskID), Note: &an,
			After: map[string]any{"hold_id": h.ID, "action": h.Action, "status": status, "actor_id": h.ActorID}})
	}))
	if err != nil {
		return nil, err
	}
	s.view(out)
	s.publish("policy.hold", out)
	return out, nil
}

func (s *Service) setResult(ctx context.Context, id, result string) error {
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		_, err := q.Exec(`UPDATE policy_holds SET result=? WHERE id=?`, clip(result, MaxMessage), id)
		return err
	}))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Service) Sweep(ctx context.Context) (int, error) {
	now := s.now().UTC()
	var expired []*Hold
	err := s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		hs, err := queryHolds(q, `WHERE status=? AND expires_at<=? ORDER BY seq`, HoldPending, millis(now))
		if err != nil {
			return err
		}
		expired = hs
		for _, h := range hs {
			h.Status, h.DecidedAt, h.Result = HoldExpired, &now, "no owner decision before the timeout"
			if err := setHold(q, h); err != nil {
				return err
			}
		}
		return nil
	}))
	if err != nil {
		return 0, err
	}
	for _, h := range expired {
		s.publish("policy.hold", h)
		s.tell(ctx, h, fmt.Sprintf("%s (%s%s) expired without an owner decision; the action was not carried out.", h.ID, h.Action, onRef(h)))
	}
	return len(expired), nil
}

func (s *Service) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = DefaultSweep
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("policy hold sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) OwnerDecisions(ctx context.Context) ([]*decisions.Decision, error) {
	hs, err := s.Holds(ctx, HoldPending, maxList)
	if err != nil {
		return nil, err
	}
	out := make([]*decisions.Decision, 0, len(hs))
	for _, h := range hs {
		if h.Status != HoldPending {
			continue
		}
		d := &decisions.Decision{
			ID: "policy:" + h.ID, Kind: decisions.KindPolicy, TaskID: h.TaskID, BriefID: h.BriefID, AgentID: h.ActorID, CreatedAt: h.CreatedAt,
			Title:   fmt.Sprintf("Approve %s%s by %s", h.Action, onRef(h), h.ActorID),
			Summary: fmt.Sprintf("%s; approve or reject with POST /api/v1/policy-holds/%s/approve or /reject before %s", h.Message, h.ID, h.ExpiresAt.Format(time.RFC3339)),
		}
		if h.TaskID != "" {
			d.Links.Thread = model.ThreadTask + ":" + h.TaskID
		}
		out = append(out, d)
	}
	return out, nil
}

type decisionSource struct{ s *Service }

func (d decisionSource) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	return d.s.OwnerDecisions(ctx)
}

func (s *Service) DecisionSource() decisions.Contributor { return decisionSource{s} }
