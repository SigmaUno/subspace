package sqlite

import (
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const taskDefaultLimit = 1000

var (
	taskSelect = `SELECT ` + store.Tasks.Select("") + ` FROM tasks`
	taskInsert = store.Tasks.Insert(ph)
	taskUpdate = store.Tasks.Update(ph)
)

const idNum = `CAST(SUBSTR(id, INSTR(id, '-') + 1) AS INTEGER)`

const taskOrder = ` ORDER BY priority DESC, ` + idNum + `, id`

func (t *tx) InsertTask(x *model.Task) error {
	args, err := store.Tasks.InsertArgs(x)
	if err != nil {
		return err
	}
	return t.exec(taskInsert, args...)
}

func (t *tx) GetTask(id string) (*model.Task, error) {
	return get(t, store.Tasks.Scan, taskSelect+` WHERE id=?`, id)
}

func (t *tx) UpdateTask(x *model.Task) error {
	args, err := store.Tasks.UpdateArgs(x)
	if err != nil {
		return err
	}
	return t.update(taskUpdate, args...)
}

func (t *tx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	var w where
	w.eq("brief_id", f.BriefID)
	w.eq("plan_id", f.PlanID)
	w.eq("assignee", f.Assignee)
	w.eq("reviewer", f.Reviewer)
	w.eq("role", f.Role)
	if len(f.Status) > 0 {
		args := make([]any, len(f.Status))
		for i, s := range f.Status {
			args[i] = s
		}
		w.add("status IN (?"+strings.Repeat(",?", len(f.Status)-1)+")", args...)
	}
	if f.AfterID != "" {
		n := store.IDNum(f.AfterID)
		w.add(`(priority < (SELECT priority FROM tasks WHERE id=?) OR (priority = (SELECT priority FROM tasks WHERE id=?) AND (`+idNum+` > ? OR (`+idNum+` = ? AND id > ?))))`,
			f.AfterID, f.AfterID, n, n, f.AfterID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = taskDefaultLimit
	}
	q := taskSelect + w.clause() + taskOrder + w.limit(limit)
	return list(t, store.Tasks.Scan, q, w.args...)
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
