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

const briefColumns = `id, title, body, repos, status, max_cost_eur, max_parallel, allowed_providers, plan_status, created_at, cross_review, auto_done_on_merge, leader_id, quality_gates`

type briefScanner interface {
	Scan(dest ...any) error
}

func briefJSON(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func briefMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func briefScan(s briefScanner) (*model.Brief, error) {
	var (
		b                  model.Brief
		repos, allowed     string
		createdMS          int64
		maxCost            float64
		maxParallel        int
		id, title, body    string
		status, planStatus string
		crossReview        bool
		autoDone           bool
		leader             sql.NullString
		gates              string
	)
	if err := s.Scan(&id, &title, &body, &repos, &status, &maxCost, &maxParallel, &allowed, &planStatus, &createdMS, &crossReview, &autoDone, &leader, &gates); err != nil {
		return nil, err
	}
	var err error
	if b.QualityGates, err = gatesUnJSON(gates); err != nil {
		return nil, err
	}
	b.ID, b.Title, b.Body, b.Status, b.PlanStatus, b.CrossReview = id, title, body, status, planStatus, crossReview
	b.AutoDoneOnMerge = autoDone
	if leader.Valid {
		b.LeaderID = &leader.String
	}
	b.Budget = model.BriefBudget{MaxCostEUR: maxCost, MaxParallelAgent: maxParallel}
	if err := json.Unmarshal([]byte(repos), &b.Repos); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(allowed), &b.AllowedProviders); err != nil {
		return nil, err
	}
	if b.Repos == nil {
		b.Repos = []string{}
	}
	if b.AllowedProviders == nil {
		b.AllowedProviders = []string{}
	}
	if createdMS != 0 {
		b.CreatedAt = time.UnixMilli(createdMS).UTC()
	}
	return &b, nil
}

func (t *tx) InsertBrief(b *model.Brief) error {
	repos, err := briefJSON(b.Repos)
	if err != nil {
		return err
	}
	allowed, err := briefJSON(b.AllowedProviders)
	if err != nil {
		return err
	}
	gates, err := gatesJSON(b.QualityGates)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx, `INSERT INTO briefs (`+briefColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.Title, b.Body, repos, b.Status, b.Budget.MaxCostEUR, b.Budget.MaxParallelAgent,
		allowed, b.PlanStatus, briefMillis(b.CreatedAt), b.CrossReview, b.AutoDoneOnMerge, b.LeaderID, gates)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return store.ErrDuplicate
	}
	return err
}

func (t *tx) GetBrief(id string) (*model.Brief, error) {
	b, err := briefScan(t.q.QueryRowContext(t.ctx, `SELECT `+briefColumns+` FROM briefs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return b, err
}

func (t *tx) UpdateBrief(b *model.Brief) error {
	repos, err := briefJSON(b.Repos)
	if err != nil {
		return err
	}
	allowed, err := briefJSON(b.AllowedProviders)
	if err != nil {
		return err
	}
	gates, err := gatesJSON(b.QualityGates)
	if err != nil {
		return err
	}
	res, err := t.q.ExecContext(t.ctx, `UPDATE briefs SET title=?, body=?, repos=?, status=?, max_cost_eur=?,
		max_parallel=?, allowed_providers=?, plan_status=?, created_at=?, cross_review=?, auto_done_on_merge=?, leader_id=?, quality_gates=? WHERE id=?`,
		b.Title, b.Body, repos, b.Status, b.Budget.MaxCostEUR, b.Budget.MaxParallelAgent,
		allowed, b.PlanStatus, briefMillis(b.CreatedAt), b.CrossReview, b.AutoDoneOnMerge, b.LeaderID, gates, b.ID)
	if err != nil {
		return err
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

func (t *tx) ListBriefs(f store.BriefFilter) ([]*model.Brief, error) {
	q := `SELECT ` + briefColumns + ` FROM briefs WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.AfterID != "" {
		c, a := afterCreatedClause("briefs", f.AfterID)
		q += ` AND ` + c
		args = append(args, a...)
	}
	q += ` ORDER BY created_at, id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*model.Brief
	for rows.Next() {
		b, err := briefScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
