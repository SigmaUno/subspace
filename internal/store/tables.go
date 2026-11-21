package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

var Briefs = &Table[model.Brief]{Name: "briefs", Cols: []Column[model.Brief]{
	field("id", KindText, func(b *model.Brief) *string { return &b.ID }),
	field("title", KindText, func(b *model.Brief) *string { return &b.Title }),
	field("body", KindText, func(b *model.Brief) *string { return &b.Body }),
	list("repos", func(b *model.Brief) *[]string { return &b.Repos }),
	field("status", KindText, func(b *model.Brief) *string { return &b.Status }),
	field("max_cost_eur", KindReal, func(b *model.Brief) *float64 { return &b.Budget.MaxCostEUR }),
	field("max_parallel", KindInt, func(b *model.Brief) *int { return &b.Budget.MaxParallelAgent }),
	list("allowed_providers", func(b *model.Brief) *[]string { return &b.AllowedProviders }),
	field("plan_status", KindText, func(b *model.Brief) *string { return &b.PlanStatus }),
	timeCol("created_at", func(b *model.Brief) *time.Time { return &b.CreatedAt }),
	field("cross_review", KindBool, func(b *model.Brief) *bool { return &b.CrossReview }).withDefault("TRUE"),
	field("auto_done_on_merge", KindBool, func(b *model.Brief) *bool { return &b.AutoDoneOnMerge }),
	field("leader_id", KindNullText, func(b *model.Brief) **string { return &b.LeaderID }),
	codec("quality_gates", KindText,
		func(b *model.Brief) (any, error) { return gatesJSON(b.QualityGates) },
		func(b *model.Brief, v string) (err error) { b.QualityGates, err = parseGates(v); return err },
	).withDefault("'{}'"),
}}

var Tasks = &Table[model.Task]{Name: "tasks", Cols: []Column[model.Task]{
	field("id", KindText, func(x *model.Task) *string { return &x.ID }),
	field("brief_id", KindText, func(x *model.Task) *string { return &x.BriefID }),
	field("plan_id", KindText, func(x *model.Task) *string { return &x.PlanID }),
	field("title", KindText, func(x *model.Task) *string { return &x.Title }),
	field("spec", KindText, func(x *model.Task) *string { return &x.Spec }),
	list("acceptance", func(x *model.Task) *[]string { return &x.AcceptanceCriteria }),
	field("repo", KindText, func(x *model.Task) *string { return &x.Repo }),
	field("branch", KindText, func(x *model.Task) *string { return &x.Branch }),
	field("role", KindText, func(x *model.Task) *string { return &x.Role }),
	optText("assignee", func(x *model.Task) **string { return &x.Assignee }),
	optText("reviewer", func(x *model.Task) **string { return &x.Reviewer }),
	list("depends_on", func(x *model.Task) *[]string { return &x.DependsOn }),
	field("status", KindText, func(x *model.Task) *string { return &x.Status }),
	field("progress_pct", KindInt, func(x *model.Task) *int { return &x.ProgressPct }),
	optTime("last_heartbeat_at", func(x *model.Task) **time.Time { return &x.LastHeartbeatAt }),
	optTime("lease_expires_at", func(x *model.Task) **time.Time { return &x.LeaseExpiresAt }),
	optTime("claimed_at", func(x *model.Task) **time.Time { return &x.ClaimedAt }),
	optText("pr_url", func(x *model.Task) **string { return &x.PRURL }),
	field("max_minutes", KindInt, func(x *model.Task) *int { return &x.Budget.MaxMinutes }),
	field("max_cost_eur", KindReal, func(x *model.Task) *float64 { return &x.Budget.MaxCostEUR }),
	field("cost_used", KindReal, func(x *model.Task) *float64 { return &x.CostUsed }),
	field("minutes_used", KindInt, func(x *model.Task) *int { return &x.MinutesUsed }),
	field("review_rounds", KindInt, func(x *model.Task) *int { return &x.ReviewRounds }),
	codec("tests_passed", KindInt,
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
	).withDefault("-1"),
	testsField("tests_failed", KindInt,
		func(r *model.TestResult) any { return r.Failed },
		func(r *model.TestResult, v int64) { r.Failed = int(v) }),
	testsField("tests_at", KindMillis,
		func(r *model.TestResult) any { return Millis(r.ReportedAt) },
		func(r *model.TestResult, v int64) { r.ReportedAt = FromMillis(v) }),
	optText("blocked_reason", func(x *model.Task) **string { return &x.BlockedReason }),
	timeCol("created_at", func(x *model.Task) *time.Time { return &x.CreatedAt }),
	timeCol("updated_at", func(x *model.Task) *time.Time { return &x.UpdatedAt }),
	field("priority", KindInt, func(x *model.Task) *int { return &x.Priority }),
	optText("component_id", func(x *model.Task) **string { return &x.ComponentID }),
	field("lease_expiries", KindInt, func(x *model.Task) *int { return &x.LeaseExpiries }),
	codec("paths", KindJSON,
		func(x *model.Task) (any, error) { return listJSON(x.Paths) },
		func(x *model.Task, v string) error {
			p, err := parseList(v)
			if err == nil && len(p) > 0 {
				x.Paths = p
			}
			return err
		}),
}}

func testsField(name string, kind ColumnKind, get func(*model.TestResult) any, set func(*model.TestResult, int64)) Column[model.Task] {
	return codec(name, kind,
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
	field("id", KindText, func(a *model.Agent) *string { return &a.ID }),
	field("role", KindText, func(a *model.Agent) *string { return &a.Role }),
	field("kind", KindText, func(a *model.Agent) *string { return &a.Kind }),
	field("model", KindText, func(a *model.Agent) *string { return &a.Model }),
	field("provider", KindText, func(a *model.Agent) *string { return &a.Provider }),
	field("status", KindText, func(a *model.Agent) *string { return &a.Status }),
	optText("pause_reason", func(a *model.Agent) **string { return &a.PauseReason }),
	field("token_hash", KindText, func(a *model.Agent) *string { return &a.TokenHash }).
		withSet(`COALESCE(NULLIF(?, ''), token_hash)`),
	optTime("last_seen_at", func(a *model.Agent) **time.Time { return &a.LastSeenAt }),
	codec("created_at", KindMillis,
		func(a *model.Agent) (any, error) {
			if a.CreatedAt.IsZero() {
				return time.Now().UnixMilli(), nil
			}
			return a.CreatedAt.UnixMilli(), nil
		},
		func(a *model.Agent, v int64) error { a.CreatedAt = FromMillis(v); return nil },
	).immutable(),
	field("spawned", KindBool, func(a *model.Agent) *bool { return &a.Spawned }),
	optTime("token_expires_at", func(a *model.Agent) **time.Time { return &a.TokenExpiresAt }),
	computed(agentAssignedCount, func(a *model.Agent, v int) { a.AssignedCount = v }),
	computed(agentCurrentTask, func(a *model.Agent, v sql.NullString) {
		if v.Valid {
			a.CurrentTaskID = &v.String
		}
	}),
}}

var Plans = &Table[model.Plan]{Name: "plans", Cols: []Column[model.Plan]{
	field("id", KindText, func(p *model.Plan) *string { return &p.ID }),
	field("brief_id", KindText, func(p *model.Plan) *string { return &p.BriefID }),
	field("status", KindText, func(p *model.Plan) *string { return &p.Status }),
	field("proposed_by", KindText, func(p *model.Plan) *string { return &p.ProposedBy }),
	codec("note", KindNullText,
		func(p *model.Plan) (any, error) {
			if s := Str(p.Note); s != "" {
				return s, nil
			}
			return nil, nil
		},
		func(p *model.Plan, v sql.NullString) error { p.Note = strPtr(v.String); return nil }),
	timeCol("created_at", func(p *model.Plan) *time.Time { return &p.CreatedAt }),
	optTime("decided_at", func(p *model.Plan) **time.Time { return &p.DecidedAt }),
	codec("warnings", KindJSON,
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
	field("id", KindText, func(q *model.Question) *string { return &q.ID }),
	optText("brief_id", func(q *model.Question) **string { return &q.BriefID }),
	optText("task_id", func(q *model.Question) **string { return &q.TaskID }),
	field("agent_id", KindText, func(q *model.Question) *string { return &q.AgentID }),
	field("question", KindText, func(q *model.Question) *string { return &q.Question }),
	list("options", func(q *model.Question) *[]string { return &q.Options }),
	field("status", KindText, func(q *model.Question) *string { return &q.Status }),
	optText("answer", func(q *model.Question) **string { return &q.Answer }),
	optTime("answered_at", func(q *model.Question) **time.Time { return &q.AnsweredAt }),
	timeCol("created_at", func(q *model.Question) *time.Time { return &q.CreatedAt }),
}}

var Spawns = &Table[model.Spawn]{Name: "spawns", Cols: []Column[model.Spawn]{
	field("id", KindText, func(s *model.Spawn) *string { return &s.ID }),
	field("brief_id", KindText, func(s *model.Spawn) *string { return &s.BriefID }),
	field("agent_id", KindText, func(s *model.Spawn) *string { return &s.AgentID }),
	field("kind", KindText, func(s *model.Spawn) *string { return &s.Kind }),
	field("model", KindText, func(s *model.Spawn) *string { return &s.Model }),
	field("provider", KindText, func(s *model.Spawn) *string { return &s.Provider }),
	field("status", KindText, func(s *model.Spawn) *string { return &s.Status }),
	field("spawner", KindText, func(s *model.Spawn) *string { return &s.Spawner }),
	optText("error", func(s *model.Spawn) **string { return &s.Error }),
	field("requested_by", KindText, func(s *model.Spawn) *string { return &s.RequestedBy }),
	timeCol("created_at", func(s *model.Spawn) *time.Time { return &s.CreatedAt }),
}}

var CoreTables = []Schema{Briefs, Tasks, Agents, Plans, Questions, Spawns}

func gatesJSON(q model.QualityGates) (string, error) {
	if !q.Enabled() {
		return "{}", nil
	}
	b, err := json.Marshal(q)
	return string(b), err
}

func parseGates(s string) (model.QualityGates, error) {
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

func parseAuditJSON(s string) (map[string]any, error) {
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
	if e.Before, err = parseAuditJSON(before.String); err != nil {
		return nil, err
	}
	if e.After, err = parseAuditJSON(after.String); err != nil {
		return nil, err
	}
	e.TS = FromMillis(ts)
	e.TaskID = strPtr(task)
	e.Note = strPtr(note)
	return &e, nil
}
