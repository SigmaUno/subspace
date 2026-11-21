package store

import (
	"errors"
	"fmt"
	"sort"
)

type SQLiteOnly struct {
	Name    string
	Reason  string
	Schema  []string
	Enabled func(getenv func(string) string) bool
}

var sqliteOnly []SQLiteOnly

func DeclareSQLiteOnly(f SQLiteOnly) { sqliteOnly = append(sqliteOnly, f) }

func SQLiteOnlySchema() map[string]string {
	out := map[string]string{}
	for _, f := range sqliteOnly {
		for _, k := range f.Schema {
			out[k] = f.Name
		}
	}
	return out
}

func SQLiteOnlyFeatures() []SQLiteOnly {
	out := append([]SQLiteOnly(nil), sqliteOnly...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func CheckSQLiteOnly(getenv func(string) string) error {
	var errs []error
	for _, f := range SQLiteOnlyFeatures() {
		if f.Enabled != nil && f.Enabled(getenv) {
			errs = append(errs, fmt.Errorf("%s is not available with a Postgres SUBSPACE_DB: %s", f.Name, f.Reason))
		}
	}
	return errors.Join(errs...)
}
