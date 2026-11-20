package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
)

const briefCols = `id, title, body, repos, status, max_cost_eur, max_parallel, allowed_providers, plan_status, created_at`

func scanBrief(r scanner) (*model.Brief, error) {
	var (
		b              model.Brief
		repos, allowed []byte
		created        int64
	)
	if err := r.Scan(&b.ID, &b.Title, &b.Body, &repos, &b.Status, &b.Budget.MaxCostEUR, &b.Budget.MaxParallelAgent,
		&allowed, &b.PlanStatus, &created); err != nil {
		return nil, err
	}
	var err error
	if b.Repos, err = fromJSON(repos); err != nil {
		return nil, err
	}
	if b.AllowedProviders, err = fromJSON(allowed); err != nil {
		return nil, err
	}
	b.CreatedAt = fromMS(created)
	return &b, nil
}

func briefArgs(b *model.Brief) ([]any, error) {
	repos, err := toJSON(b.Repos)
	if err != nil {
		return nil, err
	}
	allowed, err := toJSON(b.AllowedProviders)
	if err != nil {
		return nil, err
	}
	return []any{b.ID, b.Title, b.Body, repos, b.Status, b.Budget.MaxCostEUR, b.Budget.MaxParallelAgent,
		allowed, b.PlanStatus, ms(b.CreatedAt)}, nil
}

func (t *tx) InsertBrief(b *model.Brief) error {
	a, err := briefArgs(b)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO briefs(`+briefCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a...)
}

func (t *tx) GetBrief(id string) (*model.Brief, error) {
	b, err := scanBrief(t.q.QueryRowContext(t.ctx, `SELECT `+briefCols+` FROM briefs WHERE id = $1`, id))
	return b, mapErr(err)
}

func (t *tx) UpdateBrief(b *model.Brief) error {
	a, err := briefArgs(b)
	if err != nil {
		return err
	}
	return t.update(`UPDATE briefs SET title=$2, body=$3, repos=$4, status=$5, max_cost_eur=$6, max_parallel=$7,
  allowed_providers=$8, plan_status=$9, created_at=$10 WHERE id=$1`, a...)
}

func (t *tx) ListBriefs(status string) ([]*model.Brief, error) {
	var a args
	a.eq("status", status)
	return list(t, scanBrief, `SELECT `+briefCols+` FROM briefs`+a.clause()+` ORDER BY created_at, id`, a.vals...)
}
