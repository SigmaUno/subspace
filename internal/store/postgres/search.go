package postgres

import (
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

const like = `ILIKE $1 ESCAPE '\'`

var searchSources = []struct {
	kind  string
	query string
}{
	{"task", `SELECT 'task' AS kind, id::text AS id,
		CASE WHEN title ` + like + ` THEN 'title' ELSE 'spec' END AS field,
		CASE WHEN title ` + like + ` THEN title ELSE spec END AS text,
		updated_at AS at,
		CASE WHEN title ` + like + ` THEN 3 ELSE 1 END AS weight
		FROM tasks WHERE title ` + like + ` OR spec ` + like},
	{"message", `SELECT 'message' AS kind, id::text AS id, 'body' AS field, body AS text, created_at AS at, 1 AS weight
		FROM messages WHERE body ` + like},
	{"agent", `SELECT 'agent' AS kind, id::text AS id,
		CASE WHEN id ` + like + ` THEN 'id' ELSE 'model' END AS field,
		CASE WHEN id ` + like + ` THEN id::text ELSE model END AS text,
		GREATEST(last_seen_at, created_at) AS at,
		CASE WHEN id ` + like + ` THEN 3 ELSE 1 END AS weight
		FROM agents WHERE id ` + like + ` OR model ` + like},
	{"brief", `SELECT 'brief' AS kind, id::text AS id, 'title' AS field, title AS text, created_at AS at, 3 AS weight
		FROM briefs WHERE title ` + like},
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

var _ store.SearchTx = (*tx)(nil)

func (t *tx) Search(q store.SearchQuery) ([]store.SearchHit, error) {
	var parts []string
	for _, s := range searchSources {
		if q.Kind == "" || q.Kind == s.kind {
			parts = append(parts, s.query)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	sql := `SELECT kind, id, field, text, at, weight FROM (` + strings.Join(parts, " UNION ALL ") + `) hits
		ORDER BY at DESC, weight DESC, kind COLLATE "C", id COLLATE "C" LIMIT $2`
	rows, err := t.q.QueryContext(t.ctx, sql, "%"+likeEscaper.Replace(q.Text)+"%", q.Limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.SearchHit
	for rows.Next() {
		var h store.SearchHit
		var at int64
		if err := rows.Scan(&h.Kind, &h.ID, &h.Field, &h.Text, &at, &h.Weight); err != nil {
			return nil, err
		}
		h.At = time.UnixMilli(at).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}
