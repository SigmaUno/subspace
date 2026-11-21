package planlint_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/SigmaUno/subspace/internal/planlint"
)

func reachability(deps [][]int) [][]bool {
	n := len(deps)
	reach := make([][]bool, n)
	for i := range reach {
		reach[i] = make([]bool, n)
		for _, j := range deps[i] {
			reach[i][j] = true
		}
	}
	for k := 0; k < n; k++ {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if reach[i][k] && reach[k][j] {
					reach[i][j] = true
				}
			}
		}
	}
	return reach
}

func refOf(i int) string { return fmt.Sprintf("t%d", i) }

func TestCycleFindingsMatchBruteForce(t *testing.T) {
	for seed := int64(1); seed <= 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 1 + r.Intn(9)
		deps := make([][]int, n)
		tasks := make([]planlint.Task, n)
		for i := range tasks {
			var refs []string
			for j := 0; j < n; j++ {
				if r.Intn(n+1) == 0 {
					deps[i] = append(deps[i], j)
					refs = append(refs, refOf(j))
				}
			}
			if r.Intn(8) == 0 {
				refs = append(refs, "T-unknown")
			}
			tasks[i] = good(refOf(i), refs...)
		}

		reach := reachability(deps)
		cycle, stuck := map[string]bool{}, map[string]bool{}
		for i := 0; i < n; i++ {
			if reach[i][i] {
				cycle[refOf(i)] = true
				continue
			}
			for j := 0; j < n; j++ {
				if reach[i][j] && reach[j][j] {
					stuck[refOf(i)] = true
				}
			}
		}

		errs := rules(planlint.Lint(brief(), tasks), planlint.SeverityError)
		for rule, want := range map[string]map[string]bool{planlint.RuleCycle: cycle, planlint.RuleUnreachable: stuck} {
			got := map[string]bool{}
			for _, ref := range errs[rule] {
				got[ref] = true
			}
			if len(got) != len(errs[rule]) || len(got) != len(want) {
				t.Fatalf("seed %d: %s flagged %v, brute force says %v (deps %v)", seed, rule, errs[rule], want, deps)
			}
			for ref := range want {
				if !got[ref] {
					t.Fatalf("seed %d: %s missed %s (deps %v)", seed, rule, ref, deps)
				}
			}
		}
	}
}

func TestRandomDAGsHaveNoErrors(t *testing.T) {
	for seed := int64(1); seed <= 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 1 + r.Intn(12)
		tasks := make([]planlint.Task, n)
		for i := range tasks {
			var refs []string
			for j := 0; j < i; j++ {
				if r.Intn(3) == 0 {
					refs = append(refs, refOf(j))
				}
			}
			tasks[i] = good(refOf(i), refs...)
		}
		r.Shuffle(n, func(i, j int) { tasks[i], tasks[j] = tasks[j], tasks[i] })

		if errs, _ := planlint.Split(planlint.Lint(brief(), tasks)); len(errs) > 0 {
			t.Fatalf("seed %d: DAG rejected: %s", seed, planlint.Format(errs))
		}
	}
}
