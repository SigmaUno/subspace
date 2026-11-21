package board

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestHasCycleMatchesClosure(t *testing.T) {
	for seed := int64(1); seed <= 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := r.Intn(9)
		deps := make([][]bool, n)
		tasks := make([]*model.Task, n)
		for i := range tasks {
			deps[i] = make([]bool, n)
			tasks[i] = &model.Task{ID: fmt.Sprintf("T-%d", i)}
			for j := 0; j < n; j++ {
				if r.Intn(n+2) == 0 {
					deps[i][j] = true
					tasks[i].DependsOn = append(tasks[i].DependsOn, fmt.Sprintf("T-%d", j))
				}
			}
			if r.Intn(6) == 0 {
				tasks[i].DependsOn = append(tasks[i].DependsOn, "T-missing")
			}
		}

		for k := 0; k < n; k++ {
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					deps[i][j] = deps[i][j] || (deps[i][k] && deps[k][j])
				}
			}
		}
		want := false
		for i := 0; i < n; i++ {
			want = want || deps[i][i]
		}

		if got := hasCycle(tasks); got != want {
			t.Fatalf("seed %d: hasCycle = %v, closure says %v", seed, got, want)
		}
	}
}

func TestHasCycleFalseOnShuffledDAGs(t *testing.T) {
	for seed := int64(1); seed <= 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 1 + r.Intn(15)
		tasks := make([]*model.Task, n)
		for i := range tasks {
			tasks[i] = &model.Task{ID: fmt.Sprintf("T-%d", i)}
			for j := 0; j < i; j++ {
				if r.Intn(3) == 0 {
					tasks[i].DependsOn = append(tasks[i].DependsOn, fmt.Sprintf("T-%d", j))
				}
			}
		}
		r.Shuffle(n, func(i, j int) { tasks[i], tasks[j] = tasks[j], tasks[i] })
		if hasCycle(tasks) {
			t.Fatalf("seed %d: DAG reported as cyclic", seed)
		}
	}
}
