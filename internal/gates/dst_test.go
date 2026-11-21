package gates_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/scope"
)

func TestSimNoApprovalWithFailingGate(t *testing.T) {
	for _, seed := range []uint64{1, 7, 42} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			simulate(t, seed)
		})
	}
}

func simulate(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	coin := func(p float64) bool { return rng.Float64() < p }
	f := newFixture(t)
	cfg := map[string]any{"tests": coin(.6), "checks": coin(.5), "scope": coin(.5), "questions": coin(.5), "review": coin(.7), "budget": coin(.5)}
	if cfg["tests"] == true {
		cfg["min_tests_passed"] = rng.IntN(4)
	}
	briefID := f.brief(map[string]any{"max_cost_eur": 1000, "quality_gates": cfg})
	approved, held, step := 0, 0, 0
	for n := range 6 {
		maxCost := 1000.0
		f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": maxCost})
		id, pr := f.work(briefID, map[string]any{"acceptance_criteria": []string{"a", "b"}, "spec": fmt.Sprintf("task %d", n)})
		if coin(.4) {
			f.Must(f.wk, "ask_owner", map[string]any{"task_id": id, "question": fmt.Sprintf("q %d", n)})
		}
		f.Must(f.wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 1 + rng.Float64()})
		f.submit(id, pr)
		for range 20 {
			step++
			switch rng.IntN(5) {
			case 0:
				passed, failed := rng.IntN(5), 0
				if coin(.3) {
					failed = 1 + rng.IntN(2)
				}
				f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": 1000})
				f.testerReport(briefID, pr, passed, failed)
				f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": maxCost})
			case 1:
				checks := []string{prsync.ChecksSuccess, prsync.ChecksFailure, prsync.ChecksPending}[rng.IntN(3)]
				var findings []scope.Finding
				if coin(.3) {
					findings = append(findings, scope.Finding{Rule: scope.RuleOutsidePaths, Severity: scope.SeverityError, Message: "x"})
				}
				f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.Checks = checks; s.Scope.Findings = findings })
			case 2:
				_, qs := f.REST("GET", "/api/v1/questions?status=open", nil)
				for _, q := range qs["questions"].([]any) {
					f.REST("POST", "/api/v1/questions/"+q.(map[string]any)["id"].(string)+"/answer", map[string]any{"answer": "ok"})
				}
			case 3:
				maxCost = 1 + rng.Float64()*15
				f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": maxCost})
			case 4:
				if coin(.3) {
					f.REST("POST", "/api/v1/tasks/"+id+"/gates/override", map[string]any{"reason": "sim", "gates": []string{[]string{"tests", "checks", "scope"}[rng.IntN(3)]}})
				}
			}
			verdict := []string{"pass", "fail"}[rng.IntN(2)]
			out, isErr := f.approve(id, fmt.Sprintf("step %d\ncriterion %d: %s", step, 1+rng.IntN(2), verdict))
			_, rep := f.REST("GET", "/api/v1/tasks/"+id+"/gates", nil)
			status := f.task(id)["status"]
			if !isErr {
				if status != "approved" || rep["passed"] != true {
					t.Fatalf("seed %d step %d: %s approved with failing gates: %v", seed, step, id, rep)
				}
				approved++
				break
			}
			failedGates(t, out)
			held++
			if status == "approved" {
				t.Fatalf("seed %d step %d: held approval left %s approved", seed, step, id)
			}
		}
	}
	t.Logf("seed %d: config %v, %d approved, %d held", seed, cfg, approved, held)
}
