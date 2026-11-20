package server

import (
	"strings"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func isPostgres(db string) bool {
	return strings.HasPrefix(db, "postgres://") || strings.HasPrefix(db, "postgresql://")
}

func openStore(db string) (store.Store, error) {
	if isPostgres(db) {
		return postgres.Open(db)
	}
	return sqlite.Open(db)
}

func registerSchema(ddl string, tables ...string) {
	sqlite.RegisterSchema(ddl)
	postgres.RegisterSchema(ddl, tables...)
}
