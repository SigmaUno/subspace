package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	SuitePlan       = "plan"
	SuiteReview     = "review"
	SuiteRobustness = "robustness"

	VerdictApprove = "approve"
	VerdictChanges = "changes_requested"
)

type Repo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Requirement struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Keywords []string `json:"keywords"`
	Checks   []string `json:"checks"`
	After    []string `json:"after"`
	Role     string   `json:"role"`
	Repo     string   `json:"repo"`
}

type PlanFixture struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Body         string        `json:"body"`
	Repos        []Repo        `json:"repos"`
	MaxCostEUR   float64       `json:"max_cost_eur"`
	MaxParallel  int           `json:"max_parallel_agents"`
	CrossReview  bool          `json:"cross_review"`
	MinTasks     int           `json:"min_tasks"`
	MaxTasks     int           `json:"max_tasks"`
	Requirements []Requirement `json:"requirements"`
}

type Tests struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type Defect struct {
	File      string `json:"file"`
	Match     string `json:"match"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
	Criterion int    `json:"criterion"`
	Line      int    `json:"-"`
}

type ReviewFixture struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Spec               string   `json:"spec"`
	Repo               string   `json:"repo"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Tests              *Tests   `json:"tests"`
	PRTitle            string   `json:"pr_title"`
	PRBody             string   `json:"pr_body"`
	Defects            []Defect `json:"defects"`
	Diff               string   `json:"-"`
}

func (f *ReviewFixture) Expected() string {
	if len(f.Defects) > 0 || (f.Tests != nil && f.Tests.Failed > 0) {
		return VerdictChanges
	}
	return VerdictApprove
}

func (f *ReviewFixture) unmet() map[int]bool {
	out := map[int]bool{}
	for _, d := range f.Defects {
		out[d.Criterion] = true
	}
	return out
}

type InjectionFixture struct {
	ID        string   `json:"id"`
	Suite     string   `json:"suite"`
	Base      string   `json:"base"`
	Target    string   `json:"target"`
	Payload   string   `json:"payload"`
	Forbidden []string `json:"forbidden"`
}

type Set struct {
	Plans      []*PlanFixture
	Reviews    []*ReviewFixture
	Injections []*InjectionFixture
}

func (s *Set) plan(id string) *PlanFixture {
	for _, p := range s.Plans {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Set) review(id string) *ReviewFixture {
	for _, r := range s.Reviews {
		if r.ID == id {
			return r
		}
	}
	return nil
}

var fixtureID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func Load(dir string) (*Set, error) {
	s := &Set{}
	if err := loadDir(filepath.Join(dir, "plans"), func(path string, data []byte) error {
		var f PlanFixture
		if err := strictJSON(data, &f); err != nil {
			return err
		}
		s.Plans = append(s.Plans, &f)
		return f.validate()
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(dir, "reviews"), func(path string, data []byte) error {
		var f ReviewFixture
		if err := strictJSON(data, &f); err != nil {
			return err
		}
		diff, err := os.ReadFile(strings.TrimSuffix(path, ".json") + ".diff") //nolint:gosec // fixture directory chosen by the operator
		if err != nil {
			return err
		}
		f.Diff = string(diff)
		s.Reviews = append(s.Reviews, &f)
		return f.validate()
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(dir, "injection"), func(path string, data []byte) error {
		var f InjectionFixture
		if err := strictJSON(data, &f); err != nil {
			return err
		}
		s.Injections = append(s.Injections, &f)
		return s.validateInjection(&f)
	}); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, id := range s.ids() {
		if ids[id] {
			return nil, fmt.Errorf("eval: duplicate fixture id %q", id)
		}
		ids[id] = true
	}
	return s, nil
}

func (s *Set) ids() []string {
	var out []string
	for _, p := range s.Plans {
		out = append(out, p.ID)
	}
	for _, r := range s.Reviews {
		out = append(out, r.ID)
	}
	for _, i := range s.Injections {
		out = append(out, i.ID)
	}
	return out
}

func loadDir(dir string, fn func(path string, data []byte) error) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("eval: no fixtures in %s", dir)
	}
	slices.Sort(paths)
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // fixture directory chosen by the operator
		if err != nil {
			return err
		}
		if err := fn(p, data); err != nil {
			return fmt.Errorf("eval: %s: %w", p, err)
		}
	}
	return nil
}

func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (f *PlanFixture) validate() error {
	if !fixtureID.MatchString(f.ID) {
		return fmt.Errorf("invalid id %q", f.ID)
	}
	if f.Title == "" || f.Body == "" || len(f.Repos) == 0 || len(f.Requirements) == 0 {
		return errors.New("title, body, repos and requirements are required")
	}
	if f.MinTasks < 1 || f.MaxTasks < f.MinTasks {
		return errors.New("need 1 <= min_tasks <= max_tasks")
	}
	reqs := map[string]bool{}
	for _, r := range f.Requirements {
		if !fixtureID.MatchString(r.ID) || reqs[r.ID] {
			return fmt.Errorf("requirement id %q is invalid or repeated", r.ID)
		}
		reqs[r.ID] = true
		if r.Title == "" || len(r.Keywords) == 0 || len(r.Checks) == 0 {
			return fmt.Errorf("requirement %s needs title, keywords and checks", r.ID)
		}
		if r.Repo != "" && !slices.ContainsFunc(f.Repos, func(x Repo) bool { return x.Name == r.Repo }) {
			return fmt.Errorf("requirement %s: repo %q is not a fixture repo", r.ID, r.Repo)
		}
	}
	for _, r := range f.Requirements {
		for _, a := range r.After {
			if !reqs[a] || a == r.ID {
				return fmt.Errorf("requirement %s: after %q is unknown", r.ID, a)
			}
		}
	}
	return nil
}

func (f *ReviewFixture) validate() error {
	if !fixtureID.MatchString(f.ID) {
		return fmt.Errorf("invalid id %q", f.ID)
	}
	if f.Title == "" || f.Repo == "" || len(f.AcceptanceCriteria) == 0 || strings.TrimSpace(f.Diff) == "" {
		return errors.New("title, repo, acceptance_criteria and a diff are required")
	}
	added := addedLines(f.Diff)
	for i := range f.Defects {
		d := &f.Defects[i]
		if d.Criterion < 0 || d.Criterion >= len(f.AcceptanceCriteria) {
			return fmt.Errorf("defect %d: criterion %d out of range", i, d.Criterion)
		}
		for _, l := range added[d.File] {
			if strings.Contains(l.text, d.Match) {
				d.Line = l.n
				break
			}
		}
		if d.Line == 0 {
			return fmt.Errorf("defect %d: no added line in %s contains %q", i, d.File, d.Match)
		}
	}
	return nil
}

func (s *Set) validateInjection(f *InjectionFixture) error {
	if !fixtureID.MatchString(f.ID) || strings.TrimSpace(f.Payload) == "" {
		return errors.New("id and payload are required")
	}
	switch f.Suite {
	case SuitePlan:
		if s.plan(f.Base) == nil {
			return fmt.Errorf("unknown plan fixture %q", f.Base)
		}
		if f.Target != "body_start" && f.Target != "body_end" {
			return fmt.Errorf("plan target must be body_start or body_end, got %q", f.Target)
		}
	case SuiteReview:
		r := s.review(f.Base)
		if r == nil {
			return fmt.Errorf("unknown review fixture %q", f.Base)
		}
		switch f.Target {
		case "spec", "pr_title", "pr_body":
		case "diff":
			if r.Expected() != VerdictChanges {
				return errors.New("diff injections need a base that requests changes")
			}
		default:
			return fmt.Errorf("review target must be spec, pr_title, pr_body or diff, got %q", f.Target)
		}
	default:
		return fmt.Errorf("suite must be plan or review, got %q", f.Suite)
	}
	return nil
}

type addedLine struct {
	n    int
	text string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func addedLines(diff string) map[string][]addedLine {
	out := map[string][]addedLine{}
	file, n := "", 0
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, n = "", 0
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "@@"):
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(line, "+"):
			out[file] = append(out[file], addedLine{n, line[1:]})
			n++
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, `\`):
		default:
			n++
		}
	}
	return out
}
