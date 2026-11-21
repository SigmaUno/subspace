package announce

import (
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS announcements (
  id TEXT PRIMARY KEY, body TEXT NOT NULL, brief_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS announcements_expiry ON announcements(expires_at);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS announcements (
  id TEXT COLLATE "C" PRIMARY KEY, body TEXT NOT NULL, brief_id TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS announcements_expiry ON announcements(expires_at);
`

const cols = `id,body,brief_id,created_at,expires_at`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "announcements")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("announce: store transaction does not expose SQL")

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

func insert(t store.Tx, q sqlTx, a *Announcement) error {
	id, err := t.NextID("AN")
	if err != nil {
		return err
	}
	var expires int64
	if a.ExpiresAt != nil {
		expires = a.ExpiresAt.UnixMilli()
	}
	if _, err := q.Exec(`INSERT INTO announcements(`+cols+`) VALUES(?,?,?,?,?)`,
		id, a.Text, a.BriefID, a.CreatedAt.UnixMilli(), expires); err != nil {
		return err
	}
	a.ID = id
	return nil
}

func query(q sqlTx, where string, args ...any) ([]Announcement, error) {
	rows, err := q.Query(`SELECT `+cols+` FROM announcements `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Announcement{}
	for rows.Next() {
		var (
			a                Announcement
			created, expires int64
		)
		if err := rows.Scan(&a.ID, &a.Text, &a.BriefID, &created, &expires); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(created).UTC()
		if expires > 0 {
			e := time.UnixMilli(expires).UTC()
			a.ExpiresAt = &e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func remove(q sqlTx, id string) error {
	res, err := q.Exec(`DELETE FROM announcements WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, model.NotFound("announcement %s not found", id))
	}
	return nil
}
