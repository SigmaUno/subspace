package estimate

import (
	"math"
	"slices"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	BudgetMinutes = "max_minutes"
	BudgetCost    = "max_cost_eur"
)

type Range struct {
	Low      float64 `json:"low"`
	Expected float64 `json:"expected"`
	High     float64 `json:"high"`
}

type Totals struct {
	Tasks               int   `json:"tasks"`
	Estimated           int   `json:"estimated"`
	Minutes             Range `json:"minutes"`
	CriticalPathMinutes Range `json:"critical_path_minutes"`
	CostEUR             Range `json:"cost_eur"`
	Tokens              Range `json:"tokens"`
}

type TaskEstimate struct {
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	Role          string   `json:"role"`
	Estimate      Estimate `json:"estimate"`
	ExceedsBudget []string `json:"exceeds_budget"`
}

type Report struct {
	ID         string         `json:"id"`
	Sufficient bool           `json:"sufficient"`
	History    int            `json:"history"`
	Tasks      []TaskEstimate `json:"tasks"`
	Totals     Totals         `json:"totals"`
}

func Exceeds(t *model.Task, e Estimate) []string {
	out := []string{}
	if !e.Sufficient {
		return out
	}
	if t.Budget.MaxMinutes > 0 && e.Minutes.Median > float64(t.Budget.MaxMinutes) {
		out = append(out, BudgetMinutes)
	}
	if t.Budget.MaxCostEUR > 0 && e.CostEUR.Median > t.Budget.MaxCostEUR {
		out = append(out, BudgetCost)
	}
	return out
}

func Summarize(id string, tasks []*model.Task, estimates map[string]Estimate, history int) Report {
	live := make([]*model.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status != model.StatusCancelled {
			live = append(live, t)
		}
	}
	slices.SortStableFunc(live, func(a, b *model.Task) int { return compareIDs(a.ID, b.ID) })
	r := Report{ID: id, History: history, Tasks: make([]TaskEstimate, 0, len(live))}
	for _, t := range live {
		e := estimates[t.ID]
		if e.Basis == nil {
			e.Basis = []Level{}
		}
		if e.Confidence == "" {
			e.Confidence = ConfidenceNone
		}
		r.Tasks = append(r.Tasks, TaskEstimate{TaskID: t.ID, Title: t.Title, Role: t.Role, Estimate: e, ExceedsBudget: Exceeds(t, e)})
	}
	r.Totals = total(live, estimates)
	r.Sufficient = r.Totals.Estimated > 0
	return r
}

func total(tasks []*model.Task, estimates map[string]Estimate) Totals {
	t := Totals{Tasks: len(tasks)}
	var minutes, cost, tokens []Stat
	for _, task := range tasks {
		e, ok := estimates[task.ID]
		if !ok || !e.Sufficient {
			continue
		}
		t.Estimated++
		minutes = append(minutes, e.Minutes)
		cost = append(cost, e.CostEUR)
		tokens = append(tokens, e.Tokens)
	}
	t.Minutes = sum(minutes)
	t.CostEUR = sum(cost)
	t.Tokens = sum(tokens)
	t.CriticalPathMinutes = criticalPath(tasks, estimates)
	return t
}

func sum(stats []Stat) Range {
	var med, ss float64
	for _, s := range stats {
		med += s.Median
		d := max(s.P80-s.Median, 0)
		ss += d * d
	}
	return spread(med, ss)
}

func spread(med, sumSquares float64) Range {
	half := math.Sqrt(sumSquares)
	return Range{Low: round2(max(med-half, 0)), Expected: round2(med), High: round2(med + half)}
}

func criticalPath(tasks []*model.Task, estimates map[string]Estimate) Range {
	index := make(map[string]int, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
	}
	deps := make([][]int, len(tasks))
	children := make([][]int, len(tasks))
	indeg := make([]int, len(tasks))
	for i, t := range tasks {
		for _, d := range t.DependsOn {
			if j, ok := index[d]; ok && j != i {
				deps[i] = append(deps[i], j)
				children[j] = append(children[j], i)
				indeg[i]++
			}
		}
	}
	queue := make([]int, 0, len(tasks))
	for i := range tasks {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	finish := make([]float64, len(tasks))
	prev := make([]int, len(tasks))
	best := -1
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		prev[i] = -1
		var start float64
		for _, d := range deps[i] {
			if finish[d] > start {
				start, prev[i] = finish[d], d
			}
		}
		finish[i] = start + estimates[tasks[i].ID].Minutes.Median
		if best < 0 || finish[i] > finish[best] {
			best = i
		}
		for _, c := range children[i] {
			if indeg[c]--; indeg[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	var med, ss float64
	for i := best; i >= 0; i = prev[i] {
		s := estimates[tasks[i].ID].Minutes
		med += s.Median
		d := max(s.P80-s.Median, 0)
		ss += d * d
	}
	return spread(med, ss)
}
