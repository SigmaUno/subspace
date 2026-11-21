package briefs

import (
	"embed"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

//go:embed templates/*.md
var templateFS embed.FS

var placeholder = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

type Template struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Variables []string `json:"variables"`
	Markdown  string   `json:"markdown"`
}

func Templates() []Template {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		panic(err)
	}
	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		data, err := fs.ReadFile(templateFS, path.Join("templates", e.Name()))
		if err != nil {
			panic(err)
		}
		src := string(data)
		t := Template{ID: strings.TrimSuffix(e.Name(), ".md"), Variables: []string{}, Markdown: src}
		for _, m := range placeholder.FindAllStringSubmatch(src, -1) {
			if !slices.Contains(t.Variables, m[1]) {
				t.Variables = append(t.Variables, m[1])
			}
		}
		t.Title = templateTitle(t.ID)
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func templateTitle(id string) string {
	words := strings.Split(id, "-")
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func Render(id string, vars map[string]string) (string, error) {
	all := Templates()
	i := slices.IndexFunc(all, func(t Template) bool { return t.ID == id })
	if i < 0 {
		return "", model.NotFound("brief template %s not found", id)
	}
	t := all[i]
	var missing []string
	for _, v := range t.Variables {
		if _, ok := vars[v]; !ok {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return "", model.BadRequest("brief template %s: missing variables: %s", id, strings.Join(missing, ", "))
	}
	head, body, ok := strings.Cut(strings.TrimPrefix(t.Markdown, "---\n"), "\n---\n")
	if !ok {
		return "", model.Errf("internal", "brief template %s has no frontmatter", id)
	}
	var bad string
	head = placeholder.ReplaceAllStringFunc(head, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v := vars[name]
		if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			bad = name
		}
		return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)
	})
	if bad != "" {
		return "", model.BadRequest("brief template %s: variable %s must be a single line without control characters", id, bad)
	}
	body = placeholder.ReplaceAllStringFunc(body, func(m string) string {
		return vars[placeholder.FindStringSubmatch(m)[1]]
	})
	return "---\n" + head + "\n---\n" + body, nil
}
