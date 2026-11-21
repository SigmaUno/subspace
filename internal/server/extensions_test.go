package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
	"github.com/SigmaUno/subspace/internal/testdb"
)

func TestEveryExtensionHasPostgresOrDeclaration(t *testing.T) {
	pgTables := postgres.SchemaTables()
	pgColumns := postgres.SchemaColumns()
	declared := store.SQLiteOnlySchema()
	for _, key := range append(sqlite.ExtensionTables(), sqlite.ExtensionColumns()...) {
		_, only := declared[key]
		switch onPG := slices.Contains(pgTables, key) || slices.Contains(pgColumns, key); {
		case !onPG && !only:
			t.Errorf("%s has neither a postgres schema nor a SQLite-only declaration", key)
		case onPG && only:
			t.Errorf("%s is declared SQLite-only (%s) but has a postgres schema", key, declared[key])
		}
	}
	for key, feature := range declared {
		if !slices.Contains(sqlite.ExtensionTables(), key) && !slices.Contains(sqlite.ExtensionColumns(), key) {
			t.Errorf("%s: %s declares a schema object that SQLite does not register", key, feature)
		}
	}
}

func TestPostgresRefusesEnabledSQLiteOnlyFeature(t *testing.T) {
	t.Setenv("SUBSPACE_DRILL_EVERY", "1h")
	_, err := New(context.Background(), Config{DBPath: "postgres://invalid.invalid/subspace", OwnerToken: "owner-token-for-tests-0123456789"})
	if err == nil || !strings.Contains(err.Error(), "SUBSPACE_DRILL_EVERY") || !strings.Contains(err.Error(), "Postgres") {
		t.Fatalf("startup error: %v", err)
	}
}

func TestPostgresBackupCoversEveryTable(t *testing.T) {
	backed := postgres.BackupTables()
	created := postgres.SchemaTables()
	for _, name := range created {
		if !slices.Contains(backed, name) {
			t.Errorf("table %s is created on postgres but missing from the backup table list", name)
		}
	}
	for _, name := range backed {
		if !slices.Contains(created, name) {
			t.Errorf("backup lists %s which no schema creates", name)
		}
	}
}

func TestPostgresColumnsMatchSQLite(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	lite, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lite.Close() })
	pg := testdb.Open(t, testdb.Path(t, "t.db"))
	want, got := columnsOf(t, lite, `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`), columnsOf(t, pg, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()`)
	for table, cols := range want {
		for col := range cols {
			if !got[table][col] {
				t.Errorf("%s.%s exists on SQLite but not on Postgres", table, col)
			}
		}
	}
}

func columnsOf(t *testing.T, st store.Store, query string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	err := st.View(context.Background(), func(tx store.Tx) error {
		rows, err := tx.(interface {
			Query(q string, args ...any) (*sql.Rows, error)
		}).Query(query)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var table, col string
			if err := rows.Scan(&table, &col); err != nil {
				return err
			}
			if out[table] == nil {
				out[table] = map[string]bool{}
			}
			out[table][col] = true
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type pgSQL interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

func TestPostgresBackupRestoreRoundTripsEveryTable(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	src := testdb.Open(t, testdb.Path(t, "t.db"))
	fixed := map[string]string{
		"hillclimb_jobs.id": "H-1", "hillclimb_attempts.job_id": "H-1",
		"retention_segments.kind": "messages", "retention_segments.status": "restored",
	}
	for _, table := range postgres.BackupTables() {
		var cols []string
		var args []any
		err := src.Atomic(ctx, func(tx store.Tx) error {
			q := tx.(pgSQL)
			var empty bool
			rows, err := q.Query(`SELECT NOT EXISTS (SELECT 1 FROM ` + table + `)`)
			if err != nil {
				return err
			}
			for rows.Next() {
				if err := rows.Scan(&empty); err != nil {
					return err
				}
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil || !empty {
				return err
			}
			rows, err = q.Query(`SELECT column_name, data_type FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position`, table)
			if err != nil {
				return err
			}
			for n := 100; rows.Next(); n++ {
				var name, typ string
				if err := rows.Scan(&name, &typ); err != nil {
					return err
				}
				cols = append(cols, name)
				switch v, ok := fixed[table+"."+name]; {
				case ok:
					args = append(args, v)
				case typ == "bigint" || typ == "integer":
					args = append(args, n)
				case typ == "double precision":
					args = append(args, float64(n)+0.5)
				case typ == "boolean":
					args = append(args, true)
				case typ == "jsonb" || typ == "json":
					args = append(args, `{"n":1}`)
				default:
					args = append(args, fmt.Sprintf("v-%s", name))
				}
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			marks := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
			_, err = q.Exec(`INSERT INTO `+table+`(`+strings.Join(cols, ",")+`) VALUES(`+marks+`)`, args...)
			return err
		})
		if err != nil {
			t.Fatalf("fill %s: %v", table, err)
		}
	}

	path := filepath.Join(t.TempDir(), "b.jsonl")
	if err := src.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	dst := testdb.Open(t, testdb.Path(t, "t.db"))
	if err := dst.(interface {
		Restore(ctx context.Context, path string) error
	}).Restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	for _, table := range postgres.BackupTables() {
		want, got := dumpTable(t, src, table), dumpTable(t, dst, table)
		if len(want) == 0 {
			t.Errorf("%s: nothing was backed up", table)
		}
		if !slices.Equal(want, got) {
			t.Errorf("%s: restored %v, want %v", table, got, want)
		}
	}
}

func dumpTable(t *testing.T, st store.Store, table string) []string {
	t.Helper()
	var out []string
	err := st.View(context.Background(), func(tx store.Tx) error {
		rows, err := tx.(pgSQL).Query(`SELECT row_to_json(x)::text FROM ` + table + ` x ORDER BY 1`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
