package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const taskDefaultLimit = 1000

const taskCols = `id, brief_id, plan_id, title, spec, acceptance, repo, branch, role, assignee, reviewer, depends_on,
  status, progress_pct, last_heartbeat_at, lease_expires_at, claimed_at, pr_url, max_minutes, max_cost_eur,
  cost_used, minutes_used, review_rounds, tests_passed, tests_failed, tests_at, blocked_reason, created_at, updated_at, priority, component_id, lease_expiries`

const taskOrder = ` ORDER BY priority DESC, CAST(SUBSTR(id, INSTR(id, '-') + 1) AS INTEGER), id`

func taskMs(tm time.Time) int64 {
	if tm.IsZero() {
		return 0
	}
	return tm.UnixMilli()
}

func taskMsPtr(tm *time.Time) int64 {
	if tm == nil {
		return 0
	}
	return taskMs(*tm)
}

func taskTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func taskTimePtr(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	tm := time.UnixMilli(ms).UTC()
	return &tm
}

func taskStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func taskStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func taskJSON(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func taskUnJSON(s string) ([]string, error) {
	out := []string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func taskErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return store.ErrDuplicate
	}
	return err
}

func taskArgs(x *model.Task) ([]any, error) {
	acc, err := taskJSON(x.AcceptanceCriteria)
	if err != nil {
		return nil, err
	}
	deps, err := taskJSON(x.DependsOn)
	if err != nil {
		return nil, err
	}
	testsPassed, testsFailed, testsAt := int64(-1), int64(0), int64(0)
	if x.Tests != nil {
		testsPassed, testsFailed, testsAt = int64(x.Tests.Passed), int64(x.Tests.Failed), taskMs(x.Tests.ReportedAt)
	}
	return []any{
		x.ID, x.BriefID, x.PlanID, x.Title, x.Spec, acc, x.Repo, x.Branch, x.Role,
		taskStr(x.Assignee), taskStr(x.Reviewer), deps,
		x.Status, x.ProgressPct, taskMsPtr(x.LastHeartbeatAt), taskMsPtr(x.LeaseExpiresAt), taskMsPtr(x.ClaimedAt),
		taskStr(x.PRURL), x.Budget.MaxMinutes, x.Budget.MaxCostEUR,
		x.CostUsed, x.MinutesUsed, x.ReviewRounds, testsPassed, testsFailed, testsAt,
		taskStr(x.BlockedReason), taskMs(x.CreatedAt), taskMs(x.UpdatedAt),
		x.Priority, taskStr(x.ComponentID), x.LeaseExpiries,
	}, nil
}

type taskScanner interface {
	Scan(dest ...any) error
}

func taskScan(r taskScanner) (*model.Task, error) {
	var (
		x                                     model.Task
		acc, deps, assignee, reviewer, pr, br string
		component                             string
		hb, lease, claimed, testsAt, cAt, uAt int64
		testsPassed, testsFailed              int64
	)
	err := r.Scan(&x.ID, &x.BriefID, &x.PlanID, &x.Title, &x.Spec, &acc, &x.Repo, &x.Branch, &x.Role,
		&assignee, &reviewer, &deps,
		&x.Status, &x.ProgressPct, &hb, &lease, &claimed, &pr, &x.Budget.MaxMinutes, &x.Budget.MaxCostEUR,
		&x.CostUsed, &x.MinutesUsed, &x.ReviewRounds, &testsPassed, &testsFailed, &testsAt,
		&br, &cAt, &uAt, &x.Priority, &component, &x.LeaseExpiries)
	if err != nil {
		return nil, err
	}
	if x.AcceptanceCriteria, err = taskUnJSON(acc); err != nil {
		return nil, err
	}
	if x.DependsOn, err = taskUnJSON(deps); err != nil {
		return nil, err
	}
	x.Assignee = taskStrPtr(assignee)
	x.Reviewer = taskStrPtr(reviewer)
	x.PRURL = taskStrPtr(pr)
	x.BlockedReason = taskStrPtr(br)
	x.ComponentID = taskStrPtr(component)
	x.LastHeartbeatAt = taskTimePtr(hb)
	x.LeaseExpiresAt = taskTimePtr(lease)
	x.ClaimedAt = taskTimePtr(claimed)
	if testsPassed >= 0 {
		x.Tests = &model.TestResult{Passed: int(testsPassed), Failed: int(testsFailed), ReportedAt: taskTime(testsAt)}
	}
	x.CreatedAt = taskTime(cAt)
	x.UpdatedAt = taskTime(uAt)
	return &x, nil
}

func (t *tx) InsertTask(x *model.Task) error {
	args, err := taskArgs(x)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx,
		`INSERT INTO tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return taskErr(err)
}

func (t *tx) GetTask(id string) (*model.Task, error) {
	x, err := taskScan(t.q.QueryRowContext(t.ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return x, err
}

func (t *tx) UpdateTask(x *model.Task) error {
	args, err := taskArgs(x)
	if err != nil {
		return err
	}
	args = append(args[1:], x.ID)
	res, err := t.q.ExecContext(t.ctx, `UPDATE tasks SET brief_id=?, plan_id=?, title=?, spec=?, acceptance=?, repo=?,
  branch=?, role=?, assignee=?, reviewer=?, depends_on=?, status=?, progress_pct=?, last_heartbeat_at=?,
  lease_expires_at=?, claimed_at=?, pr_url=?, max_minutes=?, max_cost_eur=?, cost_used=?, minutes_used=?,
  review_rounds=?, tests_passed=?, tests_failed=?, tests_at=?, blocked_reason=?, created_at=?, updated_at=?, priority=?, component_id=?, lease_expiries=?
  WHERE id=?`, args...)
	if err != nil {
		return taskErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (t *tx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	var (
		where []string
		args  []any
	)
	add := func(col, v string) {
		if v != "" {
			where = append(where, col+"=?")
			args = append(args, v)
		}
	}
	add("brief_id", f.BriefID)
	add("plan_id", f.PlanID)
	add("assignee", f.Assignee)
	add("reviewer", f.Reviewer)
	add("role", f.Role)
	if len(f.Status) > 0 {
		where = append(where, "status IN (?"+strings.Repeat(",?", len(f.Status)-1)+")")
		for _, s := range f.Status {
			args = append(args, s)
		}
	}
	q := `SELECT ` + taskCols + ` FROM tasks`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = taskDefaultLimit
	}
	q += taskOrder + ` LIMIT ?`
	args = append(args, limit)

	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Task{}
	for rows.Next() {
		x, err := taskScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (t *tx) CountTasksByStatus() (map[string]int, error) {
	rows, err := t.q.QueryContext(t.ctx, `SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var (
			s string
			n int
		)
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

func (t *tx) BriefCost(briefID string) (float64, error) {
	var c float64
	err := t.q.QueryRowContext(t.ctx,
		`SELECT COALESCE(SUM(cost_used), 0) FROM tasks WHERE brief_id=?`, briefID).Scan(&c)
	return c, err
}

func (t *tx) ActiveAgents(briefID, agentID string) (int, bool, error) {
	var (
		n    int
		self bool
	)
	err := t.q.QueryRowContext(t.ctx,
		`SELECT COUNT(DISTINCT assignee), COALESCE(MAX(assignee=?), 0) FROM tasks WHERE brief_id=? AND status=?`,
		agentID, briefID, model.StatusInProgress).Scan(&n, &self)
	return n, self, err
}

func (t *tx) ExtendLeases(rows []store.LeaseRow) error {
	for _, r := range rows {
		_, err := t.q.ExecContext(t.ctx,
			`UPDATE tasks SET last_heartbeat_at=?, lease_expires_at=?
  WHERE id=? AND assignee=? AND status='in_progress' AND lease_expires_at<?`,
			r.Heartbeat, r.Lease, r.TaskID, r.Assignee, r.Lease)
		if err != nil {
			return err
		}
	}
	return nil
}
