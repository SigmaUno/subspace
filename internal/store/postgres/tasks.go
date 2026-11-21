package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const taskDefaultLimit = 1000

const taskCols = `id, brief_id, plan_id, title, spec, acceptance, repo, branch, role, assignee, reviewer, depends_on,
  status, progress_pct, last_heartbeat_at, lease_expires_at, claimed_at, pr_url, max_minutes, max_cost_eur,
  cost_used, minutes_used, review_rounds, tests_passed, tests_failed, tests_at, blocked_reason, created_at, updated_at`

const idNum = `COALESCE(substring(id from '^[^-]*-([0-9]{1,18})')::bigint, 0)`

const taskOrder = ` ORDER BY ` + idNum + `, id`

func taskArgs(x *model.Task) ([]any, error) {
	acc, err := toJSON(x.AcceptanceCriteria)
	if err != nil {
		return nil, err
	}
	deps, err := toJSON(x.DependsOn)
	if err != nil {
		return nil, err
	}
	passed, failed, at := -1, 0, int64(0)
	if x.Tests != nil {
		passed, failed, at = x.Tests.Passed, x.Tests.Failed, ms(x.Tests.ReportedAt)
	}
	return []any{
		x.ID, x.BriefID, x.PlanID, x.Title, x.Spec, acc, x.Repo, x.Branch, x.Role,
		str(x.Assignee), str(x.Reviewer), deps,
		x.Status, x.ProgressPct, msPtr(x.LastHeartbeatAt), msPtr(x.LeaseExpiresAt), msPtr(x.ClaimedAt),
		str(x.PRURL), x.Budget.MaxMinutes, x.Budget.MaxCostEUR,
		x.CostUsed, x.MinutesUsed, x.ReviewRounds, passed, failed, at,
		str(x.BlockedReason), ms(x.CreatedAt), ms(x.UpdatedAt),
	}, nil
}

func scanTask(r scanner) (*model.Task, error) {
	var (
		x                            model.Task
		acc, deps                    []byte
		assignee, reviewer, pr, br   string
		hb, lease, claimed, at, c, u int64
		passed, failed               int
	)
	err := r.Scan(&x.ID, &x.BriefID, &x.PlanID, &x.Title, &x.Spec, &acc, &x.Repo, &x.Branch, &x.Role,
		&assignee, &reviewer, &deps,
		&x.Status, &x.ProgressPct, &hb, &lease, &claimed, &pr, &x.Budget.MaxMinutes, &x.Budget.MaxCostEUR,
		&x.CostUsed, &x.MinutesUsed, &x.ReviewRounds, &passed, &failed, &at,
		&br, &c, &u)
	if err != nil {
		return nil, err
	}
	if x.AcceptanceCriteria, err = fromJSON(acc); err != nil {
		return nil, err
	}
	if x.DependsOn, err = fromJSON(deps); err != nil {
		return nil, err
	}
	x.Assignee = strPtr(assignee)
	x.Reviewer = strPtr(reviewer)
	x.PRURL = strPtr(pr)
	x.BlockedReason = strPtr(br)
	x.LastHeartbeatAt = fromMSPtr(hb)
	x.LeaseExpiresAt = fromMSPtr(lease)
	x.ClaimedAt = fromMSPtr(claimed)
	if passed >= 0 {
		x.Tests = &model.TestResult{Passed: passed, Failed: failed, ReportedAt: fromMS(at)}
	}
	x.CreatedAt = fromMS(c)
	x.UpdatedAt = fromMS(u)
	return &x, nil
}

func (t *tx) InsertTask(x *model.Task) error {
	a, err := taskArgs(x)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO tasks(`+taskCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
  $16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`, a...)
}

func (t *tx) GetTask(id string) (*model.Task, error) {
	x, err := scanTask(t.q.QueryRowContext(t.ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1`, id))
	return x, mapErr(err)
}

func (t *tx) UpdateTask(x *model.Task) error {
	a, err := taskArgs(x)
	if err != nil {
		return err
	}
	return t.update(`UPDATE tasks SET brief_id=$2, plan_id=$3, title=$4, spec=$5, acceptance=$6, repo=$7, branch=$8,
  role=$9, assignee=$10, reviewer=$11, depends_on=$12, status=$13, progress_pct=$14, last_heartbeat_at=$15,
  lease_expires_at=$16, claimed_at=$17, pr_url=$18, max_minutes=$19, max_cost_eur=$20, cost_used=$21,
  minutes_used=$22, review_rounds=$23, tests_passed=$24, tests_failed=$25, tests_at=$26, blocked_reason=$27,
  created_at=$28, updated_at=$29 WHERE id=$1`, a...)
}

func (t *tx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	var a args
	a.eq("brief_id", f.BriefID)
	a.eq("plan_id", f.PlanID)
	a.eq("assignee", f.Assignee)
	a.eq("reviewer", f.Reviewer)
	a.eq("role", f.Role)
	if len(f.Status) > 0 {
		a.where = append(a.where, "status = ANY("+a.add(f.Status)+")")
	}
	a.after(f.AfterID)
	limit := f.Limit
	if limit <= 0 {
		limit = taskDefaultLimit
	}
	q := `SELECT ` + taskCols + ` FROM tasks` + a.clause() + taskOrder + ` LIMIT ` + a.add(limit)
	return list(t, scanTask, q, a.vals...)
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
		`SELECT COALESCE(SUM(cost_used), 0) FROM tasks WHERE brief_id = $1 AND status <> $2`,
		briefID, model.StatusCancelled).Scan(&c)
	return c, err
}

func (t *tx) ExtendLeases(rows []store.LeaseRow) error {
	if len(rows) == 0 {
		return nil
	}
	type key struct{ id, assignee string }
	best := make(map[key]int, len(rows))
	var ids, assignees []string
	var hbs, leases []int64
	for _, r := range rows {
		k := key{r.TaskID, r.Assignee}
		if i, ok := best[k]; ok {
			if r.Lease > leases[i] {
				hbs[i], leases[i] = r.Heartbeat, r.Lease
			}
			continue
		}
		best[k] = len(ids)
		ids, assignees = append(ids, r.TaskID), append(assignees, r.Assignee)
		hbs, leases = append(hbs, r.Heartbeat), append(leases, r.Lease)
	}
	return t.exec(`UPDATE tasks t SET last_heartbeat_at = v.hb, lease_expires_at = v.lease
  FROM unnest($1::text[], $2::text[], $3::bigint[], $4::bigint[]) AS v(id, assignee, hb, lease)
  WHERE t.id = v.id AND t.assignee = v.assignee AND t.status = 'in_progress' AND t.lease_expires_at < v.lease`,
		ids, assignees, hbs, leases)
}
