package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

var Briefs = &Table[model.Brief]{Name: "briefs", Cols: []Column[model.Brief]{
	Field("id", KindText, func(b *model.Brief) *string { return &b.ID }),
	Field("title", KindText, func(b *model.Brief) *string { return &b.Title }),
	Field("body", KindText, func(b *model.Brief) *string { return &b.Body }),
	List("repos", func(b *model.Brief) *[]string { return &b.Repos }),
	Field("status", KindText, func(b *model.Brief) *string { return &b.Status }),
	Field("max_cost_eur", KindReal, func(b *model.Brief) *float64 { return &b.Budget.MaxCostEUR }),
	Field("max_parallel", KindInt, func(b *model.Brief) *int { return &b.Budget.MaxParallelAgent }),
	List("allowed_providers", func(b *model.Brief) *[]string { return &b.AllowedProviders }),
	Field("plan_status", KindText, func(b *model.Brief) *string { return &b.PlanStatus }),
	Time("created_at", func(b *model.Brief) *time.Time { return &b.CreatedAt }),
	Field("cross_review", KindBool, func(b *model.Brief) *bool { return &b.CrossReview }).DefaultValue("TRUE"),
	Field("auto_done_on_merge", KindBool, func(b *model.Brief) *bool { return &b.AutoDoneOnMerge }),
	Field("leader_id", KindNullText, func(b *model.Brief) **string { return &b.LeaderID }),
	Codec("quality_gates", KindText,
		func(b *model.Brief) (any, error) { return GatesJSON(b.QualityGates) },
		func(b *model.Brief, v string) (err error) { b.QualityGates, err = ParseGates(v); return err },
	).DefaultValue("'{}'"),
}}

var Tasks = &Table[model.Task]{Name: "tasks", Cols: []Column[model.Task]{
	Field("id", KindText, func(x *model.Task) *string { return &x.ID }),
	Field("brief_id", KindText, func(x *model.Task) *string { return &x.BriefID }),
	Field("plan_id", KindText, func(x *model.Task) *string { return &x.PlanID }),
	Field("title", KindText, func(x *model.Task) *string { return &x.Title }),
	Field("spec", KindText, func(x *model.Task) *string { return &x.Spec }),
	List("acceptance", func(x *model.Task) *[]string { return &x.AcceptanceCriteria }),
	Field("repo", KindText, func(x *model.Task) *string { return &x.Repo }),
	Field("branch", KindText, func(x *model.Task) *string { return &x.Branch }),
	Field("role", KindText, func(x *model.Task) *string { return &x.Role }),
	OptText("assignee", func(x *model.Task) **string { return &x.Assignee }),
	OptText("reviewer", func(x *model.Task) **string { return &x.Reviewer }),
	List("depends_on", func(x *model.Task) *[]string { return &x.DependsOn }),
	Field("status", KindText, func(x *model.Task) *string { return &x.Status }),
	Field("progress_pct", KindInt, func(x *model.Task) *int { return &x.ProgressPct }),
	OptTime("last_heartbeat_at", func(x *model.Task) **time.Time { return &x.LastHeartbeatAt }),
	OptTime("lease_expires_at", func(x *model.Task) **time.Time { return &x.LeaseExpiresAt }),
	OptTime("claimed_at", func(x *model.Task) **time.Time { return &x.ClaimedAt }),
	OptText("pr_url", func(x *model.Task) **string { return &x.PRURL }),
	Field("max_minutes", KindInt, func(x *model.Task) *int { return &x.Budget.MaxMinutes }),
	Field("max_cost_eur", KindReal, func(x *model.Task) *float64 { return &x.Budget.MaxCostEUR }),
	Field("cost_used", KindReal, func(x *model.Task) *float64 { return &x.CostUsed }),
	Field("minutes_used", KindInt, func(x *model.Task) *int { return &x.MinutesUsed }),
	Field("review_rounds", KindInt, func(x *model.Task) *int { return &x.ReviewRounds }),
	Codec("tests_passed", KindInt,
		func(x *model.Task) (any, error) {
			if x.Tests == nil {
				return -1, nil
			}
			return x.Tests.Passed, nil
		},
		func(x *model.Task, v int) error {
			if v >= 0 {
				x.Tests = &model.TestResult{Passed: v}
			}
			return nil
		},
	).DefaultValue("-1"),
	testsField("tests_failed", KindInt,
		func(r *model.TestResult) any { return r.Failed },
		func(r *model.TestResult, v int64) { r.Failed = int(v) }),
	testsField("tests_at", KindMillis,
		func(r *model.TestResult) any { return Millis(r.ReportedAt) },
		func(r *model.TestResult, v int64) { r.ReportedAt = FromMillis(v) }),
	OptText("blocked_reason", func(x *model.Task) **string { return &x.BlockedReason }),
	Time("created_at", func(x *model.Task) *time.Time { return &x.CreatedAt }),
	Time("updated_at", func(x *model.Task) *time.Time { return &x.UpdatedAt }),
	Field("priority", KindInt, func(x *model.Task) *int { return &x.Priority }),
	OptText("component_id", func(x *model.Task) **string { return &x.ComponentID }),
	Field("lease_expiries", KindInt, func(x *model.Task) *int { return &x.LeaseExpiries }),
	Codec("paths", KindJSON,
		func(x *model.Task) (any, error) { return ListJSON(x.Paths) },
		func(x *model.Task, v string) error {
			p, err := ParseList(v)
			if err == nil && len(p) > 0 {
				x.Paths = p
			}
			return err
		}),
}}

func testsField(name string, kind ColumnKind, get func(*model.TestResult) any, set func(*model.TestResult, int64)) Column[model.Task] {
	return Codec(name, kind,
		func(x *model.Task) (any, error) {
			if x.Tests == nil {
				return 0, nil
			}
			return get(x.Tests), nil
		},
		func(x *model.Task, v int64) error {
			if x.Tests != nil {
				set(x.Tests, v)
			}
			return nil
		})
}

const (
	agentAssignedCount = `(SELECT COUNT(*) FROM tasks t WHERE t.assignee = a.id AND t.status IN ('assigned','in_progress','changes_requested'))`
	agentCurrentTask   = `(SELECT t.id FROM tasks t WHERE t.assignee = a.id AND t.status = 'in_progress' ORDER BY t.id LIMIT 1)`
)

var Agents = &Table[model.Agent]{Name: "agents", Cols: []Column[model.Agent]{
	Field("id", KindText, func(a *model.Agent) *string { return &a.ID }),
	Field("role", KindText, func(a *model.Agent) *string { return &a.Role }),
	Field("kind", KindText, func(a *model.Agent) *string { return &a.Kind }),
	Field("model", KindText, func(a *model.Agent) *string { return &a.Model }),
	Field("provider", KindText, func(a *model.Agent) *string { return &a.Provider }),
	Field("status", KindText, func(a *model.Agent) *string { return &a.Status }),
	OptText("pause_reason", func(a *model.Agent) **string { return &a.PauseReason }),
	Field("token_hash", KindText, func(a *model.Agent) *string { return &a.TokenHash }).
		SetExpr(`COALESCE(NULLIF(?, ''), token_hash)`),
	OptTime("last_seen_at", func(a *model.Agent) **time.Time { return &a.LastSeenAt }),
	Codec("created_at", KindMillis,
		func(a *model.Agent) (any, error) {
			if a.CreatedAt.IsZero() {
				return time.Now().UnixMilli(), nil
			}
			return a.CreatedAt.UnixMilli(), nil
		},
		func(a *model.Agent, v int64) error { a.CreatedAt = FromMillis(v); return nil },
	).Fixed(),
	Field("spawned", KindBool, func(a *model.Agent) *bool { return &a.Spawned }),
	OptTime("token_expires_at", func(a *model.Agent) **time.Time { return &a.TokenExpiresAt }),
	Computed(agentAssignedCount, func(a *model.Agent, v int) { a.AssignedCount = v }),
	Computed(agentCurrentTask, func(a *model.Agent, v sql.NullString) {
		if v.Valid {
			a.CurrentTaskID = &v.String
		}
	}),
}}

var Plans = &Table[model.Plan]{Name: "plans", Cols: []Column[model.Plan]{
	Field("id", KindText, func(p *model.Plan) *string { return &p.ID }),
	Field("brief_id", KindText, func(p *model.Plan) *string { return &p.BriefID }),
	Field("status", KindText, func(p *model.Plan) *string { return &p.Status }),
	Field("proposed_by", KindText, func(p *model.Plan) *string { return &p.ProposedBy }),
	Codec("note", KindNullText,
		func(p *model.Plan) (any, error) {
			if s := Str(p.Note); s != "" {
				return s, nil
			}
			return nil, nil
		},
		func(p *model.Plan, v sql.NullString) error { p.Note = StrPtr(v.String); return nil }),
	Time("created_at", func(p *model.Plan) *time.Time { return &p.CreatedAt }),
	OptTime("decided_at", func(p *model.Plan) **time.Time { return &p.DecidedAt }),
	Codec("warnings", KindJSON,
		func(p *model.Plan) (any, error) {
			w := p.Warnings
			if w == nil {
				w = []model.PlanFinding{}
			}
			b, err := json.Marshal(w)
			return string(b), err
		},
		func(p *model.Plan, v string) error {
			if err := json.Unmarshal([]byte(v), &p.Warnings); err != nil {
				return err
			}
			if len(p.Warnings) == 0 {
				p.Warnings = nil
			}
			return nil
		}),
}}

var Questions = &Table[model.Question]{Name: "questions", Cols: []Column[model.Question]{
	Field("id", KindText, func(q *model.Question) *string { return &q.ID }),
	OptText("brief_id", func(q *model.Question) **string { return &q.BriefID }),
	OptText("task_id", func(q *model.Question) **string { return &q.TaskID }),
	Field("agent_id", KindText, func(q *model.Question) *string { return &q.AgentID }),
	Field("question", KindText, func(q *model.Question) *string { return &q.Question }),
	List("options", func(q *model.Question) *[]string { return &q.Options }),
	Field("status", KindText, func(q *model.Question) *string { return &q.Status }),
	OptText("answer", func(q *model.Question) **string { return &q.Answer }),
	OptTime("answered_at", func(q *model.Question) **time.Time { return &q.AnsweredAt }),
	Time("created_at", func(q *model.Question) *time.Time { return &q.CreatedAt }),
}}

var Spawns = &Table[model.Spawn]{Name: "spawns", Cols: []Column[model.Spawn]{
	Field("id", KindText, func(s *model.Spawn) *string { return &s.ID }),
	Field("brief_id", KindText, func(s *model.Spawn) *string { return &s.BriefID }),
	Field("agent_id", KindText, func(s *model.Spawn) *string { return &s.AgentID }),
	Field("kind", KindText, func(s *model.Spawn) *string { return &s.Kind }),
	Field("model", KindText, func(s *model.Spawn) *string { return &s.Model }),
	Field("provider", KindText, func(s *model.Spawn) *string { return &s.Provider }),
	Field("status", KindText, func(s *model.Spawn) *string { return &s.Status }),
	Field("spawner", KindText, func(s *model.Spawn) *string { return &s.Spawner }),
	OptText("error", func(s *model.Spawn) **string { return &s.Error }),
	Field("requested_by", KindText, func(s *model.Spawn) *string { return &s.RequestedBy }),
	Time("created_at", func(s *model.Spawn) *time.Time { return &s.CreatedAt }),
}}

var CoreTables = []Schema{Briefs, Tasks, Agents, Plans, Questions, Spawns}

func GatesJSON(q model.QualityGates) (string, error) {
	if !q.Enabled() {
		return "{}", nil
	}
	b, err := json.Marshal(q)
	return string(b), err
}

func ParseGates(s string) (model.QualityGates, error) {
	var q model.QualityGates
	if s == "" {
		return q, nil
	}
	err := json.Unmarshal([]byte(s), &q)
	return q, err
}

const MessageCols = `id, thread_kind, thread_id, author_id, author_role, kind, body, created_at`

func ScanMessage(r Scanner) (*model.Message, error) {
	var (
		m       model.Message
		created int64
	)
	if err := r.Scan(&m.ID, &m.ThreadKind, &m.ThreadID, &m.AuthorID, &m.AuthorRole, &m.Kind, &m.Body, &created); err != nil {
		return nil, err
	}
	m.CreatedAt = FromMillis(created)
	return &m, nil
}

const AuditCols = `id, ts, agent_id, role, tool, task_id, before, after, note, hash`

func AuditJSON(m map[string]any) (string, error) {
	if m == nil {
		return "", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func ParseAuditJSON(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func ScanAudit(r Scanner) (*model.AuditEvent, error) {
	var (
		e             model.AuditEvent
		ts            int64
		task, note    string
		before, after sql.NullString
	)
	if err := r.Scan(&e.ID, &ts, &e.AgentID, &e.Role, &e.Tool, &task, &before, &after, &note, &e.Hash); err != nil {
		return nil, err
	}
	var err error
	if e.Before, err = ParseAuditJSON(before.String); err != nil {
		return nil, err
	}
	if e.After, err = ParseAuditJSON(after.String); err != nil {
		return nil, err
	}
	e.TS = FromMillis(ts)
	e.TaskID = StrPtr(task)
	e.Note = StrPtr(note)
	return &e, nil
}
