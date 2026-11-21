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

const planCols = `id, brief_id, status, proposed_by, note, created_at, decided_at, warnings`

func planMS(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func planNote(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

type planScanner interface {
	Scan(dest ...any) error
}

func planScan(r planScanner) (*model.Plan, error) {
	var (
		p                  model.Plan
		note               sql.NullString
		createdMS, decided int64
		warnings           string
	)
	if err := r.Scan(&p.ID, &p.BriefID, &p.Status, &p.ProposedBy, &note, &createdMS, &decided, &warnings); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(warnings), &p.Warnings); err != nil {
		return nil, err
	}
	if len(p.Warnings) == 0 {
		p.Warnings = nil
	}
	if note.Valid && note.String != "" {
		n := note.String
		p.Note = &n
	}
	p.CreatedAt = time.UnixMilli(createdMS).UTC()
	if decided != 0 {
		d := time.UnixMilli(decided).UTC()
		p.DecidedAt = &d
	}
	return &p, nil
}

func planWarnings(p *model.Plan) (string, error) {
	w := p.Warnings
	if w == nil {
		w = []model.PlanFinding{}
	}
	b, err := json.Marshal(w)
	return string(b), err
}

func (t *tx) InsertPlan(p *model.Plan) error {
	warnings, err := planWarnings(p)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx,
		`INSERT INTO plans(`+planCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.BriefID, p.Status, p.ProposedBy, planNote(p.Note), p.CreatedAt.UnixMilli(), planMS(p.DecidedAt), warnings)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return store.ErrDuplicate
	}
	return err
}

func (t *tx) GetPlan(id string) (*model.Plan, error) {
	p, err := planScan(t.q.QueryRowContext(t.ctx, `SELECT `+planCols+` FROM plans WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return p, err
}

func (t *tx) UpdatePlan(p *model.Plan) error {
	warnings, err := planWarnings(p)
	if err != nil {
		return err
	}
	res, err := t.q.ExecContext(t.ctx,
		`UPDATE plans SET brief_id=?, status=?, proposed_by=?, note=?, created_at=?, decided_at=?, warnings=? WHERE id=?`,
		p.BriefID, p.Status, p.ProposedBy, planNote(p.Note), p.CreatedAt.UnixMilli(), planMS(p.DecidedAt), warnings, p.ID)
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

func (t *tx) ListPlans(briefID, status string) ([]*model.Plan, error) {
	q := `SELECT ` + planCols + ` FROM plans WHERE 1=1`
	var args []any
	if briefID != "" {
		q += ` AND brief_id=?`
		args = append(args, briefID)
	}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at, id`
	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*model.Plan
	for rows.Next() {
		p, err := planScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
