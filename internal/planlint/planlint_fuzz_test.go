package planlint_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
)

type lintInput struct {
	Brief model.Brief     `json:"brief"`
	Tasks []planlint.Task `json:"tasks"`
}

func FuzzLint(f *testing.F) {
	for _, s := range []string{
		`{}`,
		`{"brief":{"repos":["a/b"],"cross_review":true,"budget":{"max_cost_eur":10,"max_parallel_agents":1}},"tasks":[{"Ref":"a","Title":"A","Spec":"do it","AcceptanceCriteria":["done"],"Repo":"x/y","Role":"worker"}]}`,
		`{"tasks":[{"Ref":"a","DependsOn":["b"]},{"Ref":"b","DependsOn":["a"]}]}`,
		`{"tasks":[{"Ref":"a","DependsOn":["a"]},{"Ref":"a"},{"Title":"same"},{"Title":"same"}]}`,
		`{"tasks":[{"Ref":"a","DependsOn":["missing"],"AcceptanceCriteria":["unit tests verify the parser rejects bad input"],"Role":"tester"}]}`,
		`{"tasks":[{"Ref":"r","Spec":"` + strings.Repeat("é", 4100) + `"}]}`,
		`{"brief":{"budget":{"max_parallel_agents":2}},"tasks":[{"Ref":"a"},{"Ref":"b"},{"Ref":"c","DependsOn":["a","b"]}]}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var in lintInput
		if json.Unmarshal(data, &in) != nil || len(in.Tasks) > 200 {
			return
		}
		want, _ := json.Marshal(in)

		start := time.Now()
		fs := planlint.Lint(in.Brief, in.Tasks)
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("Lint took %s on %d tasks", el, len(in.Tasks))
		}
		if got, _ := json.Marshal(in); string(got) != string(want) {
			t.Fatalf("Lint mutated its input:\n%s\n%s", want, got)
		}
		if again := planlint.Lint(in.Brief, in.Tasks); !reflect.DeepEqual(fs, again) {
			t.Fatalf("Lint is not deterministic:\n%v\n%v", fs, again)
		}

		seenWarning := false
		for _, x := range fs {
			if x.Severity != planlint.SeverityError && x.Severity != planlint.SeverityWarning || x.Rule == "" || x.Message == "" {
				t.Fatalf("malformed finding %+v", x)
			}
			if x.Severity == planlint.SeverityWarning {
				seenWarning = true
			} else if seenWarning {
				t.Fatalf("error after warning: %v", fs)
			}
		}
		errs, warns := planlint.Split(fs)
		if len(errs)+len(warns) != len(fs) {
			t.Fatalf("Split lost findings: %d+%d != %d", len(errs), len(warns), len(fs))
		}
		if len(fs) > 0 && planlint.Format(fs) == "" {
			t.Fatal("Format dropped findings")
		}
	})
}
