package sqlite

import "regexp"

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
