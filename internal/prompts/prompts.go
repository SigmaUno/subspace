package prompts

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

//go:embed library/*.md
var embedded embed.FS

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	varPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)
)

type Template struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags"`
	Variables []string `json:"variables"`
	Body      string   `json:"body"`
}

type Library struct {
	dir string
}

func New(overrideDir string) *Library {
	return &Library{dir: overrideDir}
}

func (l *Library) List(tag string) ([]Template, error) {
	all, err := l.load()
	if err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(all))
	for _, t := range all {
		if tag == "" || slices.Contains(t.Tags, tag) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (l *Library) Get(id string) (Template, error) {
	all, err := l.load()
	if err != nil {
		return Template{}, err
	}
	t, ok := all[id]
	if !ok {
		return Template{}, model.NotFound("prompt %s not found", id)
	}
	return t, nil
}

func (l *Library) Render(id string, vars map[string]string) (string, error) {
	t, err := l.Get(id)
	if err != nil {
		return "", err
	}
	var missing []string
	for _, v := range t.Variables {
		if _, ok := vars[v]; !ok {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return "", model.BadRequest("prompt %s: missing variables: %s", id, strings.Join(missing, ", "))
	}
	return placeholder.ReplaceAllStringFunc(t.Body, func(m string) string {
		return vars[placeholder.FindStringSubmatch(m)[1]]
	}), nil
}

func (l *Library) load() (map[string]Template, error) {
	out := map[string]Template{}
	if err := collect(out, embedded, "library"); err != nil {
		return nil, err
	}
	if l.dir == "" {
		return out, nil
	}
	err := collect(out, os.DirFS(l.dir), ".")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

func collect(out map[string]Template, fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := fs.ReadFile(fsys, filepath.ToSlash(filepath.Join(dir, e.Name())))
		if err != nil {
			return err
		}
		t, err := parse(data)
		if err != nil {
			return model.Errf("internal", "prompt file %s: %v", e.Name(), err)
		}
		out[t.ID] = t
	}
	return nil
}

func parse(data []byte) (Template, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return Template{}, errors.New("missing frontmatter")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Template{}, errors.New("unterminated frontmatter")
	}
	var t Template
	for _, line := range strings.Split(string(head), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Template{}, errors.New("invalid frontmatter line: " + line)
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "id":
			t.ID = val
		case "title":
			t.Title = val
		case "tags":
			t.Tags = splitList(val)
		case "variables":
			t.Variables = splitList(val)
		default:
			return Template{}, errors.New("unknown frontmatter key: " + key)
		}
	}
	t.Body = strings.TrimSpace(string(body))
	return t, validate(t)
}

func splitList(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func validate(t Template) error {
	if !idPattern.MatchString(t.ID) {
		return errors.New("invalid id " + t.ID)
	}
	if t.Title == "" {
		return errors.New("missing title")
	}
	for _, v := range t.Variables {
		if !varPattern.MatchString(v) {
			return errors.New("invalid variable name " + v)
		}
	}
	for _, m := range placeholder.FindAllStringSubmatch(t.Body, -1) {
		if !slices.Contains(t.Variables, m[1]) {
			return errors.New("undeclared variable " + m[1])
		}
	}
	return nil
}
