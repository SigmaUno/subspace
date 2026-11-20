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

var identityTables = []string{"messages", "audit"}

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
		for _, name := range identityTables {
			if err := t.exec(`SELECT setval(pg_get_serial_sequence($1, 'id'), COALESCE((SELECT MAX(id) FROM `+name+`), 0) + 1, false)`,
				name); err != nil {
				return err
			}
		}
		return nil
	})
}
