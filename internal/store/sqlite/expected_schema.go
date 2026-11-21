package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

func ExpectedSchema(ctx context.Context) (map[string][]string, error) {
	dir, err := os.MkdirTemp("", "subspace-schema-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := Open(filepath.Join(dir, "schema.db"))
	if err != nil {
		return nil, err
	}
	out, err := ReadSchema(ctx, st.rdb)
	return out, errors.Join(err, st.Close())
}

func ReadSchema(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		out[table] = append(out[table], column)
	}
	for _, cols := range out {
		sort.Strings(cols)
	}
	return out, rows.Err()
}
