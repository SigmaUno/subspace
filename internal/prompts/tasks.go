package prompts

import (
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxVarBytes   = 4000
	maxTaskTitle  = 200
	maxTaskSpec   = 20000
	maxTaskVarSum = 16000
)

type Task struct {
	Title              string
	Spec               string
	AcceptanceCriteria []string
	Role               string
	Budget             model.Budget
}

func (l *Library) TaskTemplates() ([]Template, error) {
	all, err := l.List("")
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t Template) bool { return t.Kind != KindTask }), nil
}

func (l *Library) RenderTask(id string, vars map[string]string) (*Task, error) {
	all, err := l.TaskTemplates()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(t Template) bool { return t.ID == id })
	if i < 0 {
		ids := make([]string, len(all))
		for j, t := range all {
			ids[j] = t.ID
		}
		return nil, model.NotFound("task template %q not found (available: %s)", id, strings.Join(ids, ", "))
	}
	t := all[i]
	if err := checkVars(t, vars); err != nil {
		return nil, err
	}
	title := oneLine(substitute(t.TaskTitle, vars))
	if title == "" || utf8.RuneCountInString(title) > maxTaskTitle {
		return nil, model.BadRequest("task template %s: rendered title must be 1..%d characters", t.ID, maxTaskTitle)
	}
	spec := substitute(t.Body, vars)
	if utf8.RuneCountInString(spec) > maxTaskSpec {
		return nil, model.BadRequest("task template %s: rendered spec exceeds %d characters", t.ID, maxTaskSpec)
	}
	out := &Task{Title: title, Spec: spec, AcceptanceCriteria: []string{}, Role: t.Role}
	if out.Role == "" {
		out.Role = model.RoleWorker
	}
	if t.Budget != nil {
		out.Budget = *t.Budget
	}
	for _, c := range t.AcceptanceCriteria {
		out.AcceptanceCriteria = append(out.AcceptanceCriteria, oneLine(substitute(c, vars)))
	}
	return out, nil
}

func checkVars(t Template, vars map[string]string) error {
	var missing, unknown []string
	for _, v := range t.Variables {
		if strings.TrimSpace(vars[v]) == "" {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return model.BadRequest("task template %s: missing variables: %s (declared: %s)", t.ID, strings.Join(missing, ", "), strings.Join(t.Variables, ", "))
	}
	total := 0
	for name, val := range vars {
		if !slices.Contains(t.Variables, name) {
			unknown = append(unknown, name)
			continue
		}
		total += len(val)
		switch {
		case len(val) > maxVarBytes:
			return model.BadRequest("task template %s: variable %s exceeds %d bytes", t.ID, name, maxVarBytes)
		case strings.ContainsFunc(val, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }):
			return model.BadRequest("task template %s: variable %s contains a control character", t.ID, name)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return model.BadRequest("task template %s: unknown variables: %s (declared: %s)", t.ID, strings.Join(unknown, ", "), strings.Join(t.Variables, ", "))
	}
	if total > maxTaskVarSum {
		return model.BadRequest("task template %s: variables exceed %d bytes in total", t.ID, maxTaskVarSum)
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (l *Library) TaskHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /task-templates", func(w http.ResponseWriter, r *http.Request) {
		list, err := l.TaskTemplates()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": list})
	})
	return mux
}
