package postgres

import (
	"regexp"
	"slices"
)

var (
	createTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	addColumn   = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_]*)"?\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_]*)"?`)
)

func ddls() []string { return append([]string{schema}, extraSchemas...) }

func SchemaTables() []string {
	var out []string
	for _, ddl := range ddls() {
		for _, m := range createTable.FindAllStringSubmatch(ddl, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func SchemaColumns() []string {
	var out []string
	for _, ddl := range ddls() {
		for _, m := range addColumn.FindAllStringSubmatch(ddl, -1) {
			if k := m[1] + "." + m[2]; !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

func BackupTables() []string { return tables() }
