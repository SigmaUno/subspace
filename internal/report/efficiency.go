package report

import (
	"cmp"
	"slices"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const prMerged = "merged"

type PR struct {
	State     string
	Additions int
	Deletions int
	HasDiff   bool
}

type Efficiency struct {
	DoneTasks           int                `json:"done_tasks"`
	MergedTasks         int                `json:"merged_tasks"`
	CostPerDoneTask     *float64           `json:"cost_per_done_task"`
	CostPerMergedTask   *float64           `json:"cost_per_merged_task"`
	AcceptedLines       int                `json:"accepted_lines"`
	CostPerAcceptedLine *float64           `json:"cost_per_accepted_line"`
	ReviewOverheadEUR   float64            `json:"review_overhead_eur"`
	ReviewOverheadPct   float64            `json:"review_overhead_pct"`
	CancelledEUR        float64            `json:"cancelled_eur"`
	ReworkedEUR         float64            `json:"reworked_eur"`
	WastedEUR           float64            `json:"wasted_eur"`
	CostByStatus        map[string]float64 `json:"cost_by_status"`
}

type costPoint struct {
	at   time.Time
	cost float64
}

func costPoints(evs []*model.AuditEvent) []costPoint {
	sorted := slices.Clone(evs)
	slices.SortStableFunc(sorted, func(a, b *model.AuditEvent) int {
		if c := a.TS.Compare(b.TS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var out []costPoint
	for _, e := range sorted {
		if c, ok := e.After["cost_used"].(float64); ok {
			out = append(out, costPoint{e.TS, c})
		}
	}
	return out
}

func costAt(points []costPoint, at time.Time) float64 {
	var c float64
	for _, p := range points {
		if p.at.After(at) {
			break
		}
		c = p.cost
	}
	return c
}

func efficiency(tasks []*model.Task, reports []TaskReport, events map[string][]*model.AuditEvent, prs map[string]PR) Efficiency {
	e := Efficiency{CostByStatus: map[string]float64{}}
	var total, doneCost, mergedCost, lineCost float64
	var lines int
	for i, t := range tasks {
		tr := reports[i]
		total += t.CostUsed
		e.CostByStatus[t.Status] += t.CostUsed
		pr := prs[t.ID]
		merged := t.Status == model.StatusDone || pr.State == prMerged
		if t.Status == model.StatusDone {
			e.DoneTasks++
			doneCost += t.CostUsed
		}
		if merged {
			e.MergedTasks++
			mergedCost += t.CostUsed
		}
		if pr.HasDiff && (merged || t.Status == model.StatusApproved) {
			lines += pr.Additions + pr.Deletions
			lineCost += t.CostUsed
		}
		if t.Status == model.StatusCancelled {
			e.CancelledEUR += t.CostUsed
		}
		first, last := -1, -1
		for j, rv := range tr.Reviews {
			if rv.Verdict == "changes_requested" {
				if first < 0 {
					first = j
				}
				last = j
			}
		}
		if first >= 0 {
			points := costPoints(events[t.ID])
			e.ReviewOverheadEUR += max(t.CostUsed-costAt(points, tr.Reviews[first].At), 0)
			e.ReworkedEUR += costAt(points, tr.Reviews[last].At)
		}
	}
	e.AcceptedLines = lines
	e.CostPerDoneTask = ratio(doneCost, e.DoneTasks)
	e.CostPerMergedTask = ratio(mergedCost, e.MergedTasks)
	e.CostPerAcceptedLine = ratio(lineCost, lines)
	if total > 0 {
		e.ReviewOverheadPct = round(e.ReviewOverheadEUR / total * 100)
	}
	e.WastedEUR = round(e.CancelledEUR + e.ReworkedEUR)
	e.ReviewOverheadEUR = round(e.ReviewOverheadEUR)
	e.CancelledEUR = round(e.CancelledEUR)
	e.ReworkedEUR = round(e.ReworkedEUR)
	roundAll(e.CostByStatus)
	return e
}

func ratio(sum float64, n int) *float64 {
	if n == 0 {
		return nil
	}
	v := round(sum / float64(n))
	return &v
}
