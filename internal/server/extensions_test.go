package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
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
