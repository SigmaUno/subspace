package store

import "sort"

var sqliteOnly = map[string]string{}

func DeclareSQLiteOnly(extension, reason string) { sqliteOnly[extension] = reason }

func SQLiteOnly() map[string]string {
	out := make(map[string]string, len(sqliteOnly))
	for k, v := range sqliteOnly {
		out[k] = v
	}
	return out
}

func SQLiteOnlyNames() []string {
	out := make([]string, 0, len(sqliteOnly))
	for k := range sqliteOnly {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
