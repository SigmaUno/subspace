package postgres

import (
	"database/sql"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/model"
)

const planCols = `id, brief_id, status, proposed_by, note, created_at, decided_at, warnings`

func scanPlan(r scanner) (*model.Plan, error) {
	var (
		p                model.Plan
		note             sql.NullString
		created, decided int64
		warnings         []byte
	)
	if err := r.Scan(&p.ID, &p.BriefID, &p.Status, &p.ProposedBy, &note, &created, &decided, &warnings); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(warnings, &p.Warnings); err != nil {
		return nil, err
	}
	if len(p.Warnings) == 0 {
		p.Warnings = nil
	}
	p.Note = strPtr(note.String)
	p.CreatedAt = fromMS(created)
	p.DecidedAt = fromMSPtr(decided)
	return &p, nil
}

func planArgs(p *model.Plan) ([]any, error) {
	var note any
	if s := str(p.Note); s != "" {
		note = s
	}
	w := p.Warnings
	if w == nil {
		w = []model.PlanFinding{}
	}
	warnings, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	return []any{p.ID, p.BriefID, p.Status, p.ProposedBy, note, ms(p.CreatedAt), msPtr(p.DecidedAt), string(warnings)}, nil
}

func (t *tx) InsertPlan(p *model.Plan) error {
	a, err := planArgs(p)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO plans(`+planCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`, a...)
}

func (t *tx) GetPlan(id string) (*model.Plan, error) {
	p, err := scanPlan(t.q.QueryRowContext(t.ctx, `SELECT `+planCols+` FROM plans WHERE id = $1`, id))
	return p, mapErr(err)
}

func (t *tx) UpdatePlan(p *model.Plan) error {
	a, err := planArgs(p)
	if err != nil {
		return err
	}
	return t.update(`UPDATE plans SET brief_id=$2, status=$3, proposed_by=$4, note=$5, created_at=$6, decided_at=$7,
  warnings=$8::jsonb WHERE id=$1`, a...)
}

func (t *tx) ListPlans(briefID, status string) ([]*model.Plan, error) {
	var a args
	a.eq("brief_id", briefID)
	a.eq("status", status)
	return list(t, scanPlan, `SELECT `+planCols+` FROM plans`+a.clause()+` ORDER BY created_at, id`, a.vals...)
}
