package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/prompts"
)

type Prompt struct {
	Name string `json:"name"`
	Text string `json:"-"`
}

var builtinPrompts = map[string]string{
	"leader":   "go:cmd/subspace-leader/plan.go#systemPrompt",
	"reviewer": "go:cmd/subspace-reviewer/review.go#systemPrompt",
}

func LoadPrompt(root, spec string) (Prompt, error) {
	src := spec
	if b, ok := builtinPrompts[spec]; ok {
		src = b
	}
	kind, ref, ok := strings.Cut(src, ":")
	if !ok {
		return Prompt{}, fmt.Errorf("eval: prompt %q: want leader, reviewer, library:<id>, file:<path> or go:<file>#<const>", spec)
	}
	var text string
	var err error
	switch kind {
	case "library":
		var t prompts.Template
		t, err = prompts.New("").Get(ref)
		text = t.Body
	case "file":
		var b []byte
		b, err = os.ReadFile(resolve(root, ref)) //nolint:gosec // operator-supplied path
		text = string(b)
	case "go":
		file, name, found := strings.Cut(ref, "#")
		if !found {
			return Prompt{}, fmt.Errorf("eval: prompt %q: want go:<file>#<const>", spec)
		}
		text, err = goConst(resolve(root, file), name)
	default:
		return Prompt{}, fmt.Errorf("eval: prompt %q: unknown source %q", spec, kind)
	}
	if err != nil {
		return Prompt{}, fmt.Errorf("eval: prompt %q: %w", spec, err)
	}
	if strings.TrimSpace(text) == "" {
		return Prompt{}, fmt.Errorf("eval: prompt %q is empty", spec)
	}
	return Prompt{Name: spec, Text: text}, nil
}

func resolve(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func goConst(path, name string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != name || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return "", fmt.Errorf("%s: %s is not a string literal", path, name)
				}
				return strconv.Unquote(lit.Value)
			}
		}
	}
	return "", fmt.Errorf("%s: const %s not found", path, name)
}

func planSystem(p Prompt, f *PlanFixture) string {
	return strings.ReplaceAll(p.Text, "%d", strconv.Itoa(f.MaxTasks))
}

var briefTag = regexp.MustCompile(`(?i)<\s*/?\s*brief\s*>`)

func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	return briefTag.ReplaceAllString(s, "[brief]")
}

func planUser(f *PlanFixture, title, body string) string {
	var sb strings.Builder
	sb.WriteString("Plan this brief.\n\nBrief facts (from the server):\n")
	fmt.Fprintf(&sb, "- id: %s\n", f.ID)
	names := make([]string, len(f.Repos))
	for i, r := range f.Repos {
		names[i] = r.Name
	}
	repos, _ := json.Marshal(names)
	fmt.Fprintf(&sb, "- repos: %s\n", repos)
	if f.MaxCostEUR > 0 {
		fmt.Fprintf(&sb, "- remaining budget: %.2f EUR of %.2f EUR\n", f.MaxCostEUR, f.MaxCostEUR)
	} else {
		sb.WriteString("- remaining budget: not set\n")
	}
	if f.MaxParallel > 0 {
		fmt.Fprintf(&sb, "- max parallel agents: %d\n", f.MaxParallel)
	}
	fmt.Fprintf(&sb, "- cross review: %t\n", f.CrossReview)
	sb.WriteString("\n<brief>\ntitle: ")
	sb.WriteString(clean(title))
	sb.WriteString("\n\n")
	sb.WriteString(clean(body))
	if len(f.Repos) > 0 {
		sb.WriteString("\n\nRepositories:\n")
		for _, r := range f.Repos {
			fmt.Fprintf(&sb, "- %s: %s\n", r.Name, clean(r.Description))
		}
	}
	sb.WriteString("\n</brief>\n\nReply with the JSON object only.")
	return sb.String()
}

type diffFile struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type untrusted struct {
	TaskSpec      string     `json:"task_spec"`
	PRTitle       string     `json:"pr_title"`
	PRDescription string     `json:"pr_description"`
	Diff          []diffFile `json:"diff"`
}

type reviewInput struct {
	TaskID             string    `json:"task_id"`
	Title              string    `json:"title"`
	AcceptanceCriteria []string  `json:"acceptance_criteria"`
	Tests              *Tests    `json:"tests"`
	ReviewRound        int       `json:"review_round"`
	Untrusted          untrusted `json:"untrusted"`
}

type reviewCase struct {
	fixture *ReviewFixture
	spec    string
	prTitle string
	prBody  string
	diff    string
}

func (c reviewCase) user() string {
	f := c.fixture
	in := reviewInput{
		TaskID:             f.ID,
		Title:              f.Title,
		AcceptanceCriteria: f.AcceptanceCriteria,
		Tests:              f.Tests,
		ReviewRound:        1,
		Untrusted:          untrusted{TaskSpec: c.spec, PRTitle: c.prTitle, PRDescription: c.prBody, Diff: splitDiff(c.diff)},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	_ = enc.Encode(in)
	return buf.String()
}

func splitDiff(diff string) []diffFile {
	var out []diffFile
	for _, chunk := range strings.SplitAfter(diff, "\ndiff --git ") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		chunk = strings.TrimSuffix(chunk, "diff --git ")
		if len(out) > 0 {
			chunk = "diff --git " + chunk
		}
		out = append(out, diffFile{Path: diffPath(chunk), Patch: chunk})
	}
	return out
}

func diffPath(chunk string) string {
	line, _, _ := strings.Cut(chunk, "\n")
	rest, ok := strings.CutPrefix(line, "diff --git ")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}
