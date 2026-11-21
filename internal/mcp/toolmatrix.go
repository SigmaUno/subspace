package mcp

import (
	"sort"
	"strings"
)

const ToolMatrixHeading = "### Tool permissions (✓ = in `tools/list` for the role)"

func (r *Registry) Names(role string) []string {
	tools := r.List(role)
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func RenderToolMatrix(byRole map[string][]string) string {
	rows := map[string][]string{}
	for i, role := range AllRoles {
		for _, name := range byRole[role] {
			sig := rows[name]
			if sig == nil {
				sig = make([]string, len(AllRoles))
			}
			sig[i] = "✓"
			rows[name] = sig
		}
	}
	groups := map[string][]string{}
	for name, sig := range rows {
		key := strings.Join(sig, "|")
		groups[key] = append(groups[key], name)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		sort.Strings(groups[k])
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := strings.Count(keys[i], "✓"), strings.Count(keys[j], "✓")
		if ci != cj {
			return ci > cj
		}
		return keys[i] > keys[j]
	})
	var b strings.Builder
	b.WriteString(ToolMatrixHeading + "\n\n")
	b.WriteString("| Tool | " + strings.Join(AllRoles, " | ") + " |\n")
	b.WriteString("|---|" + strings.Repeat(":-:|", len(AllRoles)) + "\n")
	for _, k := range keys {
		b.WriteString("| " + strings.Join(groups[k], ", ") + " |")
		for _, cell := range strings.Split(k, "|") {
			if cell == "" {
				b.WriteString(" |")
				continue
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}
