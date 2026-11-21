package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type mapFacts map[string]Value

func (m mapFacts) Fact(name string) Value { return m[name] }

type corpusCase struct {
	Name    string         `json:"name"`
	Action  string         `json:"action"`
	Now     string         `json:"now"`
	Facts   map[string]any `json:"facts"`
	Want    string         `json:"want"`
	Applied []string       `json:"applied"`
	Matched []string       `json:"matched"`
}

type corpusFile struct {
	Policy Document     `json:"policy"`
	Lint   []Finding    `json:"lint"`
	Cases  []corpusCase `json:"cases"`
}

func TestCorpus(t *testing.T) {
	files, err := filepath.Glob("testdata/corpus/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cf corpusFile
			if err := json.Unmarshal(raw, &cf); err != nil {
				t.Fatal(err)
			}
			cp, findings := compile("PO-1", 1, cf.Policy)
			for _, want := range cf.Lint {
				if !slices.ContainsFunc(findings, func(f Finding) bool { return f.Code == want.Code && f.Rule == want.Rule }) {
					t.Errorf("lint: missing %s on %q in %+v", want.Code, want.Rule, findings)
				}
			}
			if len(cf.Lint) == 0 && HasErrors(findings) {
				t.Fatalf("unexpected lint errors: %+v", findings)
			}
			if len(cf.Cases) > 0 && cp == nil {
				t.Fatalf("policy does not compile: %+v", findings)
			}
			for _, c := range cf.Cases {
				facts := mapFacts{}
				for name, raw := range c.Facts {
					info, ok := factIndex[name]
					if !ok {
						t.Fatalf("%s: unknown fact %s", c.Name, name)
					}
					v, ok := FromAny(info.typ, raw)
					if !ok {
						t.Fatalf("%s: fact %s is not a %s", c.Name, name, info.Type)
					}
					facts[name] = v
				}
				now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
				if c.Now != "" {
					if now, err = time.Parse(time.RFC3339, c.Now); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					res := Decide([]*Compiled{cp}, c.Action, facts, now, nil)
					if res.Outcome != c.Want {
						t.Fatalf("%s: outcome %s want %s (%+v)", c.Name, res.Outcome, c.Want, res)
					}
					var applied, matched []string
					for _, m := range res.Matched {
						matched = append(matched, m.Rule)
						if m.Applied {
							applied = append(applied, m.Rule)
						}
					}
					if !slices.Equal(applied, c.Applied) {
						t.Fatalf("%s: applied %v want %v", c.Name, applied, c.Applied)
					}
					if c.Matched != nil && !slices.Equal(matched, c.Matched) {
						t.Fatalf("%s: matched %v want %v", c.Name, matched, c.Matched)
					}
				}
			}
		})
	}
}
