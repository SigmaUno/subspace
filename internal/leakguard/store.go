package leakguard

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS token_leaks (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, subject TEXT NOT NULL, channel TEXT NOT NULL,
  author TEXT NOT NULL, detected_at BIGINT NOT NULL, resolved_at BIGINT
);
`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(schema, "token_leaks")
}

type leak struct {
	ID         string
	Kind       string
	Subject    string
	Channel    string
	Author     string
	DetectedAt time.Time
}

func (g *Guard) record(tx store.Tx, kind, subject string, src board.LeakSource) (string, error) {
	q, err := store.SQLOf(tx)
	if err != nil {
		return "", err
	}
	id, err := tx.NextID("L")
	if err != nil {
		return "", err
	}
	_, err = q.Exec(`INSERT INTO token_leaks(id,kind,subject,channel,author,detected_at) VALUES(?,?,?,?,?,?)`,
		id, kind, subject, src.Channel, src.Actor.ID, g.o.Now().UnixMilli())
	return id, err
}

func (g *Guard) open(ctx context.Context) ([]leak, error) {
	return store.ViewSQL(ctx, g.o.Store, func(q store.SQL) ([]leak, error) {
		return store.QueryAll(q, scanLeak, `SELECT id,kind,subject,channel,author,detected_at FROM token_leaks WHERE resolved_at IS NULL ORDER BY detected_at, id`)
	})
}

func scanLeak(rows *sql.Rows) (leak, error) {
	var l leak
	var ms int64
	if err := rows.Scan(&l.ID, &l.Kind, &l.Subject, &l.Channel, &l.Author, &ms); err != nil {
		return leak{}, err
	}
	l.DetectedAt = time.UnixMilli(ms).UTC()
	return l, nil
}

func (g *Guard) get(ctx context.Context, id string) (*leak, bool, error) {
	var out *leak
	var resolved bool
	err := g.o.Store.View(ctx, func(tx store.Tx) error {
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		rows, err := q.Query(`SELECT id,kind,subject,channel,author,detected_at,resolved_at IS NOT NULL FROM token_leaks WHERE id = ?`, id)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			return rows.Err()
		}
		var l leak
		var ms int64
		if err := rows.Scan(&l.ID, &l.Kind, &l.Subject, &l.Channel, &l.Author, &ms, &resolved); err != nil {
			return err
		}
		l.DetectedAt = time.UnixMilli(ms).UTC()
		out = &l
		return nil
	})
	return out, resolved, err
}

func (g *Guard) resolve(tx store.Tx, id string) error {
	q, err := store.SQLOf(tx)
	if err != nil {
		return err
	}
	res, err := q.Exec(`UPDATE token_leaks SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`, g.o.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.Conflict("token leak %s is already resolved", id)
	}
	return nil
}

func (g *Guard) reopen(ctx context.Context, id string) {
	err := g.o.Store.Atomic(ctx, func(tx store.Tx) error {
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		_, err = q.Exec(`UPDATE token_leaks SET resolved_at = NULL WHERE id = ?`, id)
		return err
	})
	if err != nil {
		g.o.Log.Error("token leak reopen failed", "leak", id, "err", err)
	}
}

func (g *Guard) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	ls, err := g.open(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*decisions.Decision, 0, len(ls))
	for _, l := range ls {
		d := &decisions.Decision{
			ID: "leak:" + l.ID, Kind: decisions.KindLeak, CreatedAt: l.DetectedAt,
			Summary: fmt.Sprintf("found in a %s by %s; re-issue with POST /api/v1/token-leaks/%s/reissue", l.Channel, l.Author, l.ID),
		}
		if l.Kind == KindAgent {
			d.Title = fmt.Sprintf("Token of agent %s leaked and was revoked", l.Subject)
			d.AgentID = l.Subject
			d.Links = decisions.Links{Thread: model.ThreadAgent + ":" + l.Subject}
		} else {
			d.Title = fmt.Sprintf("API token %s leaked and was revoked", l.Subject)
		}
		out = append(out, d)
	}
	return out, nil
}
