package estimate

import (
	"cmp"
	"slices"

	"github.com/SigmaUno/subspace/internal/model"
)

func WorkMinutes(events []*model.AuditEvent) float64 {
	sorted := slices.Clone(events)
	slices.SortStableFunc(sorted, func(a, b *model.AuditEvent) int {
		if c := a.TS.Compare(b.TS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var total float64
	var start *model.AuditEvent
	for _, e := range sorted {
		to, _ := e.After["status"].(string)
		if to == "" {
			continue
		}
		switch {
		case start != nil && to != model.StatusInProgress:
			total += e.TS.Sub(start.TS).Minutes()
			start = nil
		case start == nil && to == model.StatusInProgress:
			start = e
		}
	}
	return total
}

func Completed(t *model.Task) bool {
	return t.Status == model.StatusDone || t.Status == model.StatusApproved
}

func SampleOf(t *model.Task, agent *model.Agent, minutes, tokens float64) Sample {
	k := KeyOf(t, agent)
	return Sample{
		TaskID:  t.ID,
		Role:    k.Role,
		Agent:   k.Agent,
		Repo:    k.Repo,
		Size:    k.Size,
		Minutes: minutes,
		Cost:    t.CostUsed,
		Tokens:  tokens,
		Rounds:  float64(t.ReviewRounds),
	}
}
