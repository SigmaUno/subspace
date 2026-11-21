package sqlite

import (
	"regexp"
	"slices"
)

var createTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_]*)"?`)

func ExtensionTables() []string {
	var out []string
	seen := map[string]bool{}
	for _, ddl := range extraSchemas {
		for _, m := range createTable.FindAllStringSubmatch(ddl, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

func ExtensionColumns() []string {
	out := make([]string, 0, len(extraColumns)+len(extensionColumns))
	for _, c := range append(slices.Clone(extraColumns), extensionColumns...) {
		out = append(out, c.table+"."+c.column)
	}
	return out
}
