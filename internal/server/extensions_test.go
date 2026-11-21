package server

import (
	"context"
	"database/sql"
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
