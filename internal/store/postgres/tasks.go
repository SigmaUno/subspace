package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const taskDefaultLimit = 1000

var (
	taskSelect = `SELECT ` + store.Tasks.Select("") + ` FROM tasks`
	taskInsert = store.Tasks.Insert(ph)
	taskUpdate = store.Tasks.Update(ph)
)

const idNum = `COALESCE(substring(id from '^[^-]*-([0-9]{1,18})')::bigint, 0)`

const taskOrder = ` ORDER BY priority DESC, ` + idNum + `, id`

func (t *tx) InsertTask(x *model.Task) error {
	a, err := store.Tasks.InsertArgs(x)
	if err != nil {
		return err
	}
	return t.exec(taskInsert, a...)
}

func (t *tx) GetTask(id string) (*model.Task, error) {
	return get(t, store.Tasks.Scan, taskSelect+` WHERE id = $1`, id)
}

func (t *tx) UpdateTask(x *model.Task) error {
	a, err := store.Tasks.UpdateArgs(x)
	if err != nil {
		return err
	}
	return t.update(taskUpdate, a...)
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
	if f.AfterID != "" {
		id, n := a.add(f.AfterID), a.add(store.IDNum(f.AfterID))
		prio := "(SELECT priority FROM tasks WHERE id = " + id + ")"
		a.where = append(a.where, "(priority < "+prio+" OR (priority = "+prio+" AND ("+idNum+" > "+n+" OR ("+idNum+" = "+n+" AND id > "+id+"))))")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = taskDefaultLimit
	}
	return list(t, store.Tasks.Scan, taskSelect+a.clause()+taskOrder+` LIMIT `+a.add(limit), a.vals...)
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
		`SELECT COALESCE(SUM(cost_used), 0) FROM tasks WHERE brief_id = $1`, briefID).Scan(&c)
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
