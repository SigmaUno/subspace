package postgres

import (
	"database/sql"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const planCols = `id, brief_id, status, proposed_by, note, created_at, decided_at`

func scanPlan(r scanner) (*model.Plan, error) {
	var (
		p                model.Plan
		note             sql.NullString
		created, decided int64
	)
	if err := r.Scan(&p.ID, &p.BriefID, &p.Status, &p.ProposedBy, &note, &created, &decided); err != nil {
		return nil, err
	}
	p.Note = strPtr(note.String)
	p.CreatedAt = fromMS(created)
	p.DecidedAt = fromMSPtr(decided)
	return &p, nil
}

func planArgs(p *model.Plan) []any {
	var note any
	if s := str(p.Note); s != "" {
		note = s
	}
	return []any{p.ID, p.BriefID, p.Status, p.ProposedBy, note, ms(p.CreatedAt), msPtr(p.DecidedAt)}
}

func (t *tx) InsertPlan(p *model.Plan) error {
	return t.exec(`INSERT INTO plans(`+planCols+`) VALUES($1,$2,$3,$4,$5,$6,$7)`, planArgs(p)...)
}

func (t *tx) GetPlan(id string) (*model.Plan, error) {
	p, err := scanPlan(t.q.QueryRowContext(t.ctx, `SELECT `+planCols+` FROM plans WHERE id = $1`, id))
	return p, mapErr(err)
}

func (t *tx) UpdatePlan(p *model.Plan) error {
	return t.update(`UPDATE plans SET brief_id=$2, status=$3, proposed_by=$4, note=$5, created_at=$6, decided_at=$7
  WHERE id=$1`, planArgs(p)...)
}

func (t *tx) ListPlans(f store.PlanFilter) ([]*model.Plan, error) {
	var a args
	a.eq("brief_id", f.BriefID)
	a.eq("status", f.Status)
	a.afterCreated("plans", f.AfterID)
	return list(t, scanPlan, `SELECT `+planCols+` FROM plans`+a.clause()+` ORDER BY created_at, id`+a.limit(f.Limit), a.vals...)
}
