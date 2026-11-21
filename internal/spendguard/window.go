package spendguard

import (
	"math"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

type Window string

const (
	Daily  Window = "daily"
	Weekly Window = "weekly"
)

var Windows = []Window{Daily, Weekly}

const (
	PauseReason = "org_budget"
	WarnRatio   = 0.8
)

type Level string

const (
	LevelOff      Level = "off"
	LevelOK       Level = "ok"
	LevelWarning  Level = "warning"
	LevelExceeded Level = "exceeded"
)

type Limits struct {
	DailyCostEUR  float64 `json:"daily_cost_eur"`
	WeeklyCostEUR float64 `json:"weekly_cost_eur"`
}

func (l Limits) Cap(w Window) float64 {
	if w == Weekly {
		return l.WeeklyCostEUR
	}
	return l.DailyCostEUR
}

func (l Limits) Validate() error {
	for _, w := range Windows {
		c := l.Cap(w)
		if c < 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return model.BadRequest("%s_cost_eur must be a number >= 0", w)
		}
	}
	return nil
}

type Entry struct {
	At  time.Time
	EUR float64
}

type Status struct {
	Window                Window     `json:"window"`
	CapEUR                float64    `json:"cap_eur"`
	SpentEUR              float64    `json:"spent_eur"`
	Percent               float64    `json:"percent"`
	Level                 Level      `json:"level"`
	StartsAt              time.Time  `json:"starts_at"`
	ResetsAt              time.Time  `json:"resets_at"`
	ProjectedExhaustionAt *time.Time `json:"projected_exhaustion_at"`
}

func WindowStart(w Window, now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	if w == Weekly {
		d -= (int(now.UTC().Weekday()) + 6) % 7
	}
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func WindowEnd(w Window, now time.Time) time.Time {
	y, m, d := WindowStart(w, now).Date()
	if w == Weekly {
		return time.Date(y, m, d+7, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

func Sum(entries []Entry, from, to time.Time) float64 {
	var total float64
	for _, e := range entries {
		if !e.At.Before(from) && e.At.Before(to) {
			total += e.EUR
		}
	}
	return total
}

func Evaluate(l Limits, usage, tasks []Entry, now time.Time) []Status {
	now = now.UTC()
	out := make([]Status, 0, len(Windows))
	for _, w := range Windows {
		start, end := WindowStart(w, now), WindowEnd(w, now)
		upto := now.Add(time.Nanosecond)
		if upto.After(end) {
			upto = end
		}
		spent := math.Max(Sum(usage, start, upto), Sum(tasks, start, upto))
		out = append(out, status(w, l.Cap(w), spent, start, end, now))
	}
	return out
}

func status(w Window, limit, spent float64, start, end, now time.Time) Status {
	s := Status{Window: w, CapEUR: limit, SpentEUR: spent, Level: LevelOff, StartsAt: start, ResetsAt: end}
	if limit <= 0 {
		return s
	}
	s.Percent = spent / limit * 100
	switch {
	case spent >= limit:
		s.Level = LevelExceeded
	case spent >= limit*WarnRatio:
		s.Level = LevelWarning
	default:
		s.Level = LevelOK
	}
	if elapsed := now.Sub(start); spent > 0 && spent < limit && elapsed > 0 {
		eta := now.Add(time.Duration(float64(elapsed) * (limit - spent) / spent))
		if eta.Before(end) {
			s.ProjectedExhaustionAt = &eta
		}
	}
	return s
}
