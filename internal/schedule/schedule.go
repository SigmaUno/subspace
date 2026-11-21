package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	ActionCreateBrief = "create_brief"
	ActionAddTasks    = "add_tasks"

	CatchUpSkip    = "skip"
	CatchUpRunOnce = "run-once"

	RunRunning     = "running"
	RunCreated     = "created"
	RunSkipped     = "skipped"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"

	ReasonMissed    = "missed"
	ReasonMaxActive = "max_active"

	MaxSchedules  = 100
	MaxTasks      = 20
	MaxNameLength = 100
	MaxActiveCap  = 100
	MaxPreview    = 20
	DefaultActive = 1
	runsLimit     = 100

	maxVars      = 50
	maxVarBytes  = 4000
	maxVarsTotal = 16000
)

type BriefSpec struct {
	TemplateID string            `json:"template_id"`
	Markdown   string            `json:"markdown"`
	Vars       map[string]string `json:"vars"`
}

type TaskSpec struct {
	TemplateID string            `json:"template_id"`
	Vars       map[string]string `json:"vars"`
	Repo       string            `json:"repo"`
	Priority   int               `json:"priority"`
}

type Schedule struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Cron      string     `json:"cron"`
	Timezone  string     `json:"timezone"`
	Action    string     `json:"action"`
	Brief     *BriefSpec `json:"brief"`
	BriefID   string     `json:"brief_id"`
	Tasks     []TaskSpec `json:"tasks"`
	Enabled   bool       `json:"enabled"`
	MaxActive int        `json:"max_active"`
	CatchUp   string     `json:"catch_up"`
	NextRunAt *time.Time `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	cursor time.Time
}

type Run struct {
	ScheduleID   string     `json:"schedule_id"`
	ScheduledFor time.Time  `json:"scheduled_for"`
	Status       string     `json:"status"`
	Missed       int        `json:"missed"`
	Detail       string     `json:"detail"`
	BriefID      string     `json:"brief_id"`
	TaskIDs      []string   `json:"task_ids"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

type Input struct {
	Name      string     `json:"name"`
	Cron      string     `json:"cron"`
	Timezone  string     `json:"timezone"`
	Action    string     `json:"action"`
	Brief     *BriefSpec `json:"brief"`
	BriefID   string     `json:"brief_id"`
	Tasks     []TaskSpec `json:"tasks"`
	Enabled   *bool      `json:"enabled"`
	MaxActive *int       `json:"max_active"`
	CatchUp   string     `json:"catch_up"`
}

type Patch struct {
	Name      *string     `json:"name"`
	Cron      *string     `json:"cron"`
	Timezone  *string     `json:"timezone"`
	Action    *string     `json:"action"`
	Brief     *BriefSpec  `json:"brief"`
	BriefID   *string     `json:"brief_id"`
	Tasks     *[]TaskSpec `json:"tasks"`
	Enabled   *bool       `json:"enabled"`
	MaxActive *int        `json:"max_active"`
	CatchUp   *string     `json:"catch_up"`
}

type Preview struct {
	Cron     string      `json:"cron"`
	Timezone string      `json:"timezone"`
	Next     []time.Time `json:"next"`
}

type Executor interface {
	Validate(ctx context.Context, s *Schedule) error
	CreateBrief(ctx context.Context, s *Schedule, at time.Time) (string, error)
	AddTasks(ctx context.Context, s *Schedule, at time.Time) ([]string, error)
}

type Service struct {
	st   store.Store
	exec Executor
	now  func() time.Time
}

func NewService(st store.Store, exec Executor, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{st: st, exec: exec, now: now}
}

func (s *Service) Create(ctx context.Context, in Input) (*Schedule, error) {
	now := s.now().UTC()
	sc := &Schedule{
		Name: in.Name, Cron: in.Cron, Timezone: in.Timezone, Action: in.Action, Brief: in.Brief, BriefID: in.BriefID,
		Tasks: in.Tasks, Enabled: true, MaxActive: DefaultActive, CatchUp: in.CatchUp, CreatedAt: now, UpdatedAt: now, cursor: now,
	}
	if in.Enabled != nil {
		sc.Enabled = *in.Enabled
	}
	if in.MaxActive != nil {
		sc.MaxActive = *in.MaxActive
	}
	if err := s.normalize(ctx, sc, true); err != nil {
		return nil, err
	}
	err := s.st.Atomic(ctx, store.WithSQL(func(t store.Tx, q store.SQL) error {
		n, err := count(q)
		if err != nil {
			return err
		}
		if n >= MaxSchedules {
			return model.Conflict("at most %d schedules are allowed", MaxSchedules)
		}
		if err := insert(t, q, sc); err != nil {
			return err
		}
		return s.audit(ctx, t, "create_schedule", nil, sc)
	}))
	if err != nil {
		return nil, err
	}
	s.decorate(sc, nil)
	return sc, nil
}

func (s *Service) Update(ctx context.Context, id string, p Patch) (*Schedule, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	next := *cur
	applyPatch(&next, p)
	now := s.now().UTC()
	next.UpdatedAt = now
	if next.Cron != cur.Cron || next.Timezone != cur.Timezone || (next.Enabled && !cur.Enabled) {
		next.cursor = now
	}
	retarget := p.Action != nil || p.Brief != nil || p.BriefID != nil || p.Tasks != nil
	if err := s.normalize(ctx, &next, retarget); err != nil {
		return nil, err
	}
	err = s.st.Atomic(ctx, store.WithSQL(func(t store.Tx, q store.SQL) error {
		if err := update(q, &next); err != nil {
			return err
		}
		return s.audit(ctx, t, "update_schedule", cur, &next)
	}))
	if err != nil {
		return nil, err
	}
	s.decorate(&next, next.LastRunAt)
	return &next, nil
}

func applyPatch(sc *Schedule, p Patch) {
	if p.Name != nil {
		sc.Name = *p.Name
	}
	if p.Cron != nil {
		sc.Cron = *p.Cron
	}
	if p.Timezone != nil {
		sc.Timezone = *p.Timezone
	}
	if p.Action != nil {
		sc.Action = *p.Action
	}
	if p.Brief != nil {
		sc.Brief = p.Brief
	}
	if p.BriefID != nil {
		sc.BriefID = *p.BriefID
	}
	if p.Tasks != nil {
		sc.Tasks = *p.Tasks
	}
	if p.Enabled != nil {
		sc.Enabled = *p.Enabled
	}
	if p.MaxActive != nil {
		sc.MaxActive = *p.MaxActive
	}
	if p.CatchUp != nil {
		sc.CatchUp = *p.CatchUp
	}
}

func (s *Service) normalize(ctx context.Context, sc *Schedule, check bool) error {
	sc.Name = strings.TrimSpace(sc.Name)
	switch {
	case sc.Name == "":
		return model.BadRequest("name is required")
	case utf8.RuneCountInString(sc.Name) > MaxNameLength:
		return model.BadRequest("name is longer than %d characters", MaxNameLength)
	case !utf8.ValidString(sc.Name) || strings.ContainsFunc(sc.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return model.BadRequest("name contains control characters")
	}
	sc.Cron = strings.Join(strings.Fields(sc.Cron), " ")
	if sc.Timezone = strings.TrimSpace(sc.Timezone); sc.Timezone == "" {
		sc.Timezone = "UTC"
	}
	if _, err := ParseSpec(sc.Cron, sc.Timezone); err != nil {
		return err
	}
	if sc.CatchUp == "" {
		sc.CatchUp = CatchUpSkip
	}
	if sc.CatchUp != CatchUpSkip && sc.CatchUp != CatchUpRunOnce {
		return model.BadRequest("catch_up must be %s or %s", CatchUpSkip, CatchUpRunOnce)
	}
	if sc.MaxActive < 0 || sc.MaxActive > MaxActiveCap {
		return model.BadRequest("max_active must be between 0 and %d", MaxActiveCap)
	}
	switch sc.Action {
	case ActionCreateBrief:
		if sc.Brief == nil {
			return model.BadRequest("brief is required for action %s", ActionCreateBrief)
		}
		if (sc.Brief.TemplateID == "") == (strings.TrimSpace(sc.Brief.Markdown) == "") {
			return model.BadRequest("brief needs exactly one of template_id or markdown")
		}
		if sc.Brief.Vars == nil {
			sc.Brief.Vars = map[string]string{}
		}
		if err := checkVars("brief", sc.Brief.Vars); err != nil {
			return err
		}
		sc.BriefID, sc.Tasks = "", []TaskSpec{}
	case ActionAddTasks:
		if sc.BriefID == "" {
			return model.BadRequest("brief_id is required for action %s", ActionAddTasks)
		}
		if len(sc.Tasks) == 0 || len(sc.Tasks) > MaxTasks {
			return model.BadRequest("tasks needs 1 to %d entries", MaxTasks)
		}
		for i := range sc.Tasks {
			t := &sc.Tasks[i]
			if t.TemplateID == "" || strings.TrimSpace(t.Repo) == "" {
				return model.BadRequest("tasks[%d] needs template_id and repo", i)
			}
			if t.Vars == nil {
				t.Vars = map[string]string{}
			}
			if err := checkVars(fmt.Sprintf("tasks[%d]", i), t.Vars); err != nil {
				return err
			}
		}
		sc.Brief = nil
	default:
		return model.BadRequest("action must be %s or %s", ActionCreateBrief, ActionAddTasks)
	}
	if !check {
		return nil
	}
	return s.exec.Validate(ctx, sc)
}

func checkVars(where string, vars map[string]string) error {
	if len(vars) > maxVars {
		return model.BadRequest("%s: at most %d vars are allowed", where, maxVars)
	}
	total := 0
	for k, v := range vars {
		if len(v) > maxVarBytes {
			return model.BadRequest("%s: var %s is longer than %d bytes", where, k, maxVarBytes)
		}
		total += len(k) + len(v)
	}
	if total > maxVarsTotal {
		return model.BadRequest("%s: vars are longer than %d bytes in total", where, maxVarsTotal)
	}
	return nil
}

func (s *Service) List(ctx context.Context) ([]*Schedule, error) {
	var out []*Schedule
	err := s.st.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		var err error
		out, err = list(q)
		return err
	}))
	if err != nil {
		return nil, err
	}
	for _, sc := range out {
		s.decorate(sc, sc.LastRunAt)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Schedule, error) {
	var out *Schedule
	err := s.st.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		var err error
		out, err = get(q, id)
		return err
	}))
	if err != nil {
		return nil, err
	}
	s.decorate(out, out.LastRunAt)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.st.Atomic(ctx, store.WithSQL(func(t store.Tx, q store.SQL) error {
		cur, err := get(q, id)
		if err != nil {
			return err
		}
		if err := remove(q, id); err != nil {
			return err
		}
		return s.audit(ctx, t, "delete_schedule", cur, nil)
	}))
}

func (s *Service) Runs(ctx context.Context, id string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = runsLimit
	}
	var out []Run
	err := s.st.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		if _, err := get(q, id); err != nil {
			return err
		}
		var err error
		out, err = runs(q, id, min(limit, 1000))
		return err
	}))
	return out, err
}

func (s *Service) decorate(sc *Schedule, last *time.Time) {
	sc.LastRunAt = last
	sc.NextRunAt = nil
	if !sc.Enabled {
		return
	}
	spec, err := ParseSpec(sc.Cron, sc.Timezone)
	if err != nil {
		return
	}
	if t, ok := spec.Next(s.now()); ok {
		t = t.UTC()
		sc.NextRunAt = &t
	}
}

func (s *Service) Preview(cron, tz string, count int) (*Preview, error) {
	if count <= 0 {
		count = 5
	}
	if count > MaxPreview {
		return nil, model.BadRequest("count must be between 1 and %d", MaxPreview)
	}
	if tz = strings.TrimSpace(tz); tz == "" {
		tz = "UTC"
	}
	spec, err := ParseSpec(cron, tz)
	if err != nil {
		return nil, err
	}
	out := &Preview{Cron: strings.Join(strings.Fields(cron), " "), Timezone: tz, Next: []time.Time{}}
	at := s.now()
	for range count {
		t, ok := spec.Next(at)
		if !ok {
			break
		}
		out.Next = append(out.Next, t.UTC())
		at = t
	}
	return out, nil
}

func ParseSpec(cron, tz string) (*Cron, error) {
	loc, err := LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	return ParseCron(cron, loc)
}

func (s *Service) audit(ctx context.Context, t store.Tx, tool string, before, after *Schedule) error {
	e := &model.AuditEvent{TS: s.now().UTC(), AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool}
	ref := after
	if ref == nil {
		ref = before
	}
	note := auth.AuditNote(ctx, ref.ID)
	e.Note = &note
	if before != nil {
		e.Before = summary(before)
	}
	if after != nil {
		e.After = summary(after)
	}
	return t.InsertAudit(e)
}

func summary(s *Schedule) map[string]any {
	return map[string]any{"id": s.ID, "name": s.Name, "cron": s.Cron, "timezone": s.Timezone, "action": s.Action, "enabled": s.Enabled}
}
