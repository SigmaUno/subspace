package instructions

import "strings"

type section struct {
	title string
	text  string
}

func splitSections(text string) []section {
	var out []section
	var cur []string
	title := ""
	fence := ""
	flush := func() {
		body := strings.TrimRight(strings.Join(cur, "\n"), "\n ")
		if strings.TrimSpace(body) != "" {
			out = append(out, section{title: title, text: body})
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if marker := fenceMarker(trimmed); marker != "" {
			switch fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}
		}
		if fence == "" && strings.HasPrefix(line, "## ") {
			flush()
			title = "#" + strings.ToLower(strings.TrimSpace(line[3:]))
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

func fenceMarker(line string) string {
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(line, m) {
			return m
		}
	}
	return ""
}

func Merge(layers ...string) string {
	var merged []section
	index := map[string]int{}
	for _, layer := range layers {
		for _, sec := range splitSections(layer) {
			if i, ok := index[sec.title]; ok {
				merged[i] = sec
				continue
			}
			index[sec.title] = len(merged)
			merged = append(merged, sec)
		}
	}
	parts := make([]string, len(merged))
	for i, sec := range merged {
		parts[i] = sec.text
	}
	return strings.Join(parts, "\n\n")
}
