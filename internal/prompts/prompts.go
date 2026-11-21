package prompts

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

//go:embed library/*.md
var embedded embed.FS

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	varPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)
)

const (
	KindPrompt = "prompt"
	KindTask   = "task"

	maxCriteria     = 20
	maxCriterionLen = 500
)

var (
	taskKeys      = map[string]bool{"task_title": true, "role": true, "budget_minutes": true, "budget_cost_eur": true, "acceptance_criteria": true}
	numberPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

type Template struct {
	ID                 string        `json:"id"`
	Title              string        `json:"title"`
	Tags               []string      `json:"tags"`
	Variables          []string      `json:"variables"`
	Body               string        `json:"body"`
	Kind               string        `json:"kind,omitempty"`
	TaskTitle          string        `json:"task_title,omitempty"`
	Role               string        `json:"role,omitempty"`
	Budget             *model.Budget `json:"budget,omitempty"`
	AcceptanceCriteria []string      `json:"acceptance_criteria,omitempty"`
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
	return substitute(t.Body, vars), nil
}

func substitute(s string, vars map[string]string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		return vars[placeholder.FindStringSubmatch(m)[1]]
	})
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
	t, err := parseHead(string(head))
	if err != nil {
		return Template{}, err
	}
	t.Body = strings.TrimSpace(string(body))
	return t, validate(t)
}

func parseHead(head string) (Template, error) {
	var t Template
	var minutes, cost string
	seen := map[string]bool{}
	list := ""
	for _, line := range strings.Split(head, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "-" || strings.HasPrefix(trimmed, "- ") {
			item, err := listItem(line, list)
			if err != nil {
				return Template{}, err
			}
			t.AcceptanceCriteria = append(t.AcceptanceCriteria, item)
			continue
		}
		list = ""
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Template{}, errors.New("invalid frontmatter line: " + line)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if taskKeys[key] || key == "kind" {
			if seen[key] {
				return Template{}, errors.New("duplicate frontmatter key: " + key)
			}
			seen[key] = true
		}
		switch key {
		case "id":
			t.ID = val
		case "title":
			t.Title = val
		case "tags":
			t.Tags = splitList(val)
		case "variables":
			t.Variables = splitList(val)
		case "kind":
			if val != KindPrompt && val != KindTask {
				return Template{}, errors.New("invalid kind " + val)
			}
			if val == KindTask {
				t.Kind = KindTask
			}
		case "task_title":
			t.TaskTitle = val
		case "role":
			t.Role = val
		case "budget_minutes":
			minutes = val
		case "budget_cost_eur":
			cost = val
		case "acceptance_criteria":
			if val != "" {
				return Template{}, errors.New("acceptance_criteria must be a list of \"- \" lines")
			}
			list = key
		default:
			return Template{}, errors.New("unknown frontmatter key: " + key)
		}
	}
	if seen["acceptance_criteria"] && len(t.AcceptanceCriteria) == 0 {
		return Template{}, errors.New("acceptance_criteria is empty")
	}
	if len(t.AcceptanceCriteria) > maxCriteria {
		return Template{}, fmt.Errorf("more than %d acceptance criteria", maxCriteria)
	}
	if seen["budget_minutes"] || seen["budget_cost_eur"] {
		b, err := parseBudget(seen, minutes, cost)
		if err != nil {
			return Template{}, err
		}
		t.Budget = &b
	}
	return t, nil
}

func listItem(line, list string) (string, error) {
	if list == "" {
		return "", errors.New("list item outside a list: " + strings.TrimSpace(line))
	}
	if indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]; strings.Contains(indent, "\t") {
		return "", errors.New("tab indentation in frontmatter")
	}
	item := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
	if strings.HasPrefix(item, `"`) {
		u, err := strconv.Unquote(item)
		if err != nil {
			return "", errors.New("invalid quoted list item: " + item)
		}
		item = strings.TrimSpace(u)
	}
	switch {
	case item == "":
		return "", errors.New("empty list item")
	case utf8.RuneCountInString(item) > maxCriterionLen:
		return "", fmt.Errorf("list item longer than %d characters", maxCriterionLen)
	case strings.ContainsFunc(item, unicode.IsControl):
		return "", errors.New("control character in list item")
	}
	return item, nil
}

func parseBudget(seen map[string]bool, minutes, cost string) (model.Budget, error) {
	var b model.Budget
	if seen["budget_minutes"] {
		if !numberPattern.MatchString(minutes) || strings.Contains(minutes, ".") {
			return b, errors.New("invalid budget_minutes " + minutes)
		}
		n, err := strconv.Atoi(minutes)
		if err != nil {
			return b, errors.New("invalid budget_minutes " + minutes)
		}
		b.MaxMinutes = n
	}
	if seen["budget_cost_eur"] {
		if !numberPattern.MatchString(cost) {
			return b, errors.New("invalid budget_cost_eur " + cost)
		}
		f, err := strconv.ParseFloat(cost, 64)
		if err != nil || math.IsInf(f, 0) {
			return b, errors.New("invalid budget_cost_eur " + cost)
		}
		b.MaxCostEUR = f
	}
	return b, nil
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
	texts := append([]string{t.Body, t.TaskTitle}, t.AcceptanceCriteria...)
	for _, text := range texts {
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			if !slices.Contains(t.Variables, m[1]) {
				return errors.New("undeclared variable " + m[1])
			}
		}
	}
	return validateTask(t)
}

func validateTask(t Template) error {
	if t.Kind != KindTask {
		if t.TaskTitle != "" || t.Role != "" || t.Budget != nil || t.AcceptanceCriteria != nil {
			return errors.New("task_title, role, budget_* and acceptance_criteria require kind: task")
		}
		return nil
	}
	if strings.TrimSpace(t.TaskTitle) == "" {
		return errors.New("task template needs task_title")
	}
	if strings.ContainsFunc(t.TaskTitle, unicode.IsControl) {
		return errors.New("control character in task_title")
	}
	if t.Role != "" && t.Role != model.RoleWorker && t.Role != model.RoleTester {
		return errors.New("role must be worker or tester")
	}
	return nil
}
