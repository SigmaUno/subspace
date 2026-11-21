package postgres

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/SigmaUno/subspace/internal/store"
)

const restoreChunk = 500

var coreTables = []string{"seq", "kv", "briefs", "plans", "tasks", "agents", "messages", "questions", "audit", "spawns"}

func tables() []string { return append(slices.Clip(coreTables), extraTables...) }

type backupRow struct {
	Table string          `json:"t"`
	Row   json.RawMessage `json:"r"`
}

func (s *Store) Backup(ctx context.Context, path string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is chosen by the operator
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	t, err := s.rdb.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, name := range tables() {
		if err := dumpTable(ctx, t, enc, name); err != nil {
			return fmt.Errorf("backup %s: %w", name, err)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

func dumpTable(ctx context.Context, t *sql.Tx, enc *json.Encoder, name string) error {
	rows, err := t.QueryContext(ctx, `SELECT row_to_json(x)::text FROM `+name+` x`) //nolint:gosec // name is from the registered tables list
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r []byte
		if err := rows.Scan(&r); err != nil {
			return err
		}
		if err := enc.Encode(backupRow{Table: name, Row: r}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func VerifyBackup(ctx context.Context, path string) error {
	f, err := os.Open(path) //nolint:gosec // path is chosen by the operator
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	all := tables()
	dec := json.NewDecoder(bufio.NewReader(f))
	for n := 0; ; n++ {
		if n%restoreChunk == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		var r backupRow
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("verify: row %d: %w", n, err)
		}
		if !slices.Contains(all, r.Table) {
			return fmt.Errorf("verify: row %d: unknown table %q", n, r.Table)
		}
		if len(r.Row) == 0 || r.Row[0] != '{' {
			return fmt.Errorf("verify: row %d: malformed %s row", n, r.Table)
		}
	}
}

func (s *Store) Restore(ctx context.Context, path string) error {
	f, err := os.Open(path) //nolint:gosec // path is chosen by the operator
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return s.Atomic(ctx, func(st store.Tx) error {
		t, ok := st.(*tx)
		if !ok {
			return errors.New("restore: unexpected tx type")
		}
		all := tables()
		for _, name := range all {
			var exists bool
			if err := t.q.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM `+name+`)`).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("restore: table %s is not empty", name)
			}
		}
		dec := json.NewDecoder(bufio.NewReader(f))
		var (
			cur   string
			chunk []json.RawMessage
		)
		flush := func() error {
			if len(chunk) == 0 {
				return nil
			}
			b, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			chunk = chunk[:0]
			return t.exec(`INSERT INTO `+cur+` SELECT * FROM json_populate_recordset(NULL::`+cur+`, $1::json)`, string(b))
		}
		for {
			var r backupRow
			err := dec.Decode(&r)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("restore: %w", err)
			}
			if !slices.Contains(all, r.Table) {
				return fmt.Errorf("restore: unknown table %q", r.Table)
			}
			if r.Table != cur || len(chunk) >= restoreChunk {
				if err := flush(); err != nil {
					return err
				}
				cur = r.Table
			}
			chunk = append(chunk, r.Row)
		}
		if err := flush(); err != nil {
			return err
		}
		t.touch("audit")
		identity, err := identityTables(t)
		if err != nil {
			return err
		}
		for _, c := range identity {
			if !slices.Contains(all, c.table) {
				continue
			}
			if err := t.exec(`SELECT setval(pg_get_serial_sequence($1, $2), COALESCE((SELECT MAX(`+c.column+`) FROM `+c.table+`), 0) + 1, false)`,
				c.table, c.column); err != nil {
				return err
			}
		}
		return nil
	})
}

type identityColumn struct{ table, column string }

func identityTables(t *tx) ([]identityColumn, error) {
	rows, err := t.q.QueryContext(t.ctx, `SELECT table_name, column_name FROM information_schema.columns
  WHERE table_schema = current_schema() AND is_identity = 'YES' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []identityColumn
	for rows.Next() {
		var c identityColumn
		if err := rows.Scan(&c.table, &c.column); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
