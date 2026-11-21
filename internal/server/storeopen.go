package server

import (
	"context"
	"strings"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func isPostgres(db string) bool {
	return strings.HasPrefix(db, "postgres://") || strings.HasPrefix(db, "postgresql://")
}

func openStore(db string, o store.OpenOptions) (store.Store, error) {
	if isPostgres(db) {
		return postgres.OpenWith(db, o)
	}
	return sqlite.OpenWith(db, o)
}

func backupVerifier(db string) func(context.Context, string) error {
	if isPostgres(db) {
		return postgres.VerifyBackup
	}
	return sqlite.IntegrityCheck
}

func registerSchema(ddl string, tables ...string) {
	sqlite.RegisterSchema(ddl)
	postgres.RegisterSchema(ddl, tables...)
}

func registerColumn(table, column, def string) {
	sqlite.RegisterColumn(table, column, def)
	postgres.RegisterSchema(`ALTER TABLE ` + table + ` ADD COLUMN IF NOT EXISTS ` + column + ` ` + def)
}
