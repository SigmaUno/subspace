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

func insert(t store.Tx, q store.SQL, a *Announcement) error {
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

func query(q store.SQL, where string, args ...any) ([]Announcement, error) {
	return store.QueryAll(q, scanAnnouncement, `SELECT `+cols+` FROM announcements `+where, args...)
}

func scanAnnouncement(rows *sql.Rows) (Announcement, error) {
	var (
		a                Announcement
		created, expires int64
	)
	if err := rows.Scan(&a.ID, &a.Text, &a.BriefID, &created, &expires); err != nil {
		return Announcement{}, err
	}
	a.CreatedAt = time.UnixMilli(created).UTC()
	if expires > 0 {
		e := time.UnixMilli(expires).UTC()
		a.ExpiresAt = &e
	}
	return a, nil
}

func remove(q store.SQL, id string) error {
	res, err := q.Exec(`DELETE FROM announcements WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, model.NotFound("announcement %s not found", id))
	}
	return nil
}
