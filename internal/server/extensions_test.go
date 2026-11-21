package server

import (
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func TestEveryExtensionHasPostgresOrDeclaration(t *testing.T) {
	pgTables := postgres.SchemaTables()
	pgColumns := postgres.SchemaColumns()
	declared := store.SQLiteOnly()
	for _, name := range sqlite.ExtensionTables() {
		if _, ok := declared[name]; !ok && !slices.Contains(pgTables, name) {
			t.Errorf("table %s has no postgres schema and no SQLite-only declaration", name)
		}
	}
	for _, key := range sqlite.ExtensionColumns() {
		if _, ok := declared[key]; !ok && !slices.Contains(pgColumns, key) {
			t.Errorf("column %s has no postgres schema and no SQLite-only declaration", key)
		}
	}
	for name := range declared {
		if slices.Contains(pgTables, name) || slices.Contains(pgColumns, name) {
			t.Errorf("%s is declared SQLite-only but has a postgres schema", name)
		}
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
