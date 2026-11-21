package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS knowledge (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  tags TEXT NOT NULL, author TEXT NOT NULL, author_role TEXT NOT NULL, status TEXT NOT NULL, pinned INTEGER NOT NULL,
  supersedes TEXT NOT NULL DEFAULT '', superseded_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS knowledge_brief ON knowledge(brief_id, status);
`

const cols = `id,brief_id,kind,title,body,tags,author,author_role,status,pinned,supersedes,superseded_by,created_at,updated_at`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(strings.ReplaceAll(schema, "INTEGER", "BIGINT"), "knowledge")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("knowledge: store transaction does not expose SQL")

func sqlOf(tx store.Tx) (sqlTx, error) {
	q, ok := tx.(sqlTx)
	if !ok {
		return nil, errNoSQL
	}
	return q, nil
}

func insert(tx store.Tx, e *Entry) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO knowledge(`+cols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args(e)...)
	return err
}

func update(tx store.Tx, e *Entry) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	a := args(e)
	_, err = q.Exec(`UPDATE knowledge SET brief_id=?,kind=?,title=?,body=?,tags=?,author=?,author_role=?,status=?,pinned=?,
		supersedes=?,superseded_by=?,created_at=?,updated_at=? WHERE id=?`, append(a[1:], a[0])...)
	return err
}

func remove(tx store.Tx, id string) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	_, err = q.Exec(`DELETE FROM knowledge WHERE id=?`, id)
	return err
}

func get(tx store.Tx, id string) (*Entry, error) {
	es, err := query(tx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, model.NotFound("knowledge entry %s not found", id)
	}
	return es[0], nil
}

func list(tx store.Tx, briefs []string, statuses []string) ([]*Entry, error) {
	var where []string
	var a []any
	if briefs != nil {
		if len(briefs) == 0 {
			return nil, nil
		}
		where = append(where, `brief_id IN (`+placeholders(len(briefs))+`)`)
		for _, b := range briefs {
			a = append(a, b)
		}
	}
	if len(statuses) > 0 {
		where = append(where, `status IN (`+placeholders(len(statuses))+`)`)
		for _, s := range statuses {
			a = append(a, s)
		}
	}
	clause := ""
	if len(where) > 0 {
		clause = `WHERE ` + strings.Join(where, ` AND `)
	}
	return query(tx, clause+` ORDER BY created_at DESC, length(id) DESC, id DESC`, a...)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func query(tx store.Tx, clause string, a ...any) ([]*Entry, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM knowledge `+clause, a...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Entry
	for rows.Next() {
		var e Entry
		var tags, supersedes, supersededBy string
		var pinned int
		var created, updated int64
		if err := rows.Scan(&e.ID, &e.BriefID, &e.Kind, &e.Title, &e.Body, &tags, &e.Author, &e.AuthorRole, &e.Status,
			&pinned, &supersedes, &supersededBy, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tags), &e.Tags); err != nil {
			return nil, err
		}
		e.Pinned = pinned != 0
		e.Supersedes = nullable(supersedes)
		e.SupersededBy = nullable(supersededBy)
		e.CreatedAt = time.UnixMilli(created).UTC()
		e.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, &e)
	}
	return out, rows.Err()
}

func args(e *Entry) []any {
	tags, _ := json.Marshal(e.Tags)
	pinned := 0
	if e.Pinned {
		pinned = 1
	}
	return []any{e.ID, e.BriefID, e.Kind, e.Title, e.Body, string(tags), e.Author, e.AuthorRole, e.Status, pinned,
		deref(e.Supersedes), deref(e.SupersededBy), e.CreatedAt.UnixMilli(), e.UpdatedAt.UnixMilli()}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
