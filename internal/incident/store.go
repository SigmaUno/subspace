package incident

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const cols = `id, reason, actions, results, started_at, ended_at, actor`

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("incident: store transaction does not expose SQL")

func sqlOf(tx store.Tx) (sqlTx, error) {
	q, ok := tx.(sqlTx)
	if !ok {
		return nil, errNoSQL
	}
	return q, nil
}

func SupportsSQL(ctx context.Context, st store.Store) bool {
	ok := false
	_ = st.View(ctx, func(tx store.Tx) error {
		_, ok = tx.(sqlTx)
		return nil
	})
	return ok
}

func scan(rows *sql.Rows) ([]*Incident, error) {
	defer func() { _ = rows.Close() }()
	out := []*Incident{}
	for rows.Next() {
		var (
			in               Incident
			actions, results string
			started, ended   int64
		)
		if err := rows.Scan(&in.ID, &in.Reason, &actions, &results, &started, &ended, &in.Actor); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(actions), &in.Actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(results), &in.Results); err != nil {
			return nil, err
		}
		in.StartedAt = time.UnixMilli(started).UTC()
		if ended > 0 {
			t := time.UnixMilli(ended).UTC()
			in.EndedAt = &t
		}
		out = append(out, &in)
	}
	return out, rows.Err()
}

func query(tx store.Tx, where string, args ...any) ([]*Incident, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM incidents `+where, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func loadTx(tx store.Tx, id string) (*Incident, error) {
	in, err := query(tx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return nil, model.NotFound("incident %s not found", id)
	}
	return in[0], nil
}

func activeTx(tx store.Tx) (*Incident, error) {
	in, err := query(tx, `WHERE ended_at = 0 ORDER BY started_at DESC LIMIT 1`)
	if err != nil || len(in) == 0 {
		return nil, err
	}
	return in[0], nil
}

func insertTx(tx store.Tx, in *Incident) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	actions, _ := json.Marshal(in.Actions)
	results, _ := json.Marshal(in.Results)
	_, err = q.Exec(`INSERT INTO incidents (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Reason, string(actions), string(results), in.StartedAt.UnixMilli(), endedMillis(in), in.Actor)
	return err
}

func updateTx(tx store.Tx, in *Incident) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	actions, _ := json.Marshal(in.Actions)
	results, _ := json.Marshal(in.Results)
	_, err = q.Exec(`UPDATE incidents SET actions = ?, results = ?, ended_at = ? WHERE id = ?`,
		string(actions), string(results), endedMillis(in), in.ID)
	return err
}

func endedMillis(in *Incident) int64 {
	if in.EndedAt == nil {
		return 0
	}
	return in.EndedAt.UnixMilli()
}

func (s *Service) audit(ctx context.Context, tx store.Tx, tool, id string, before, after map[string]any) error {
	note := auth.AuditNote(ctx, "incident "+id)
	return tx.InsertAudit(&model.AuditEvent{
		TS: s.now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: tool,
		Before: before, After: after, Note: &note,
	})
}

func (s *Service) List(ctx context.Context, limit int) ([]*Incident, error) {
	var out []*Incident
	err := s.o.Store.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = query(tx, `ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
		return err
	})
	return out, err
}

func (s *Service) Get(ctx context.Context, id string) (*Incident, error) {
	var out *Incident
	err := s.o.Store.View(ctx, func(tx store.Tx) (err error) { out, err = loadTx(tx, id); return })
	return out, err
}
