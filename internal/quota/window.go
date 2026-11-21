package quota

import (
	"math"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	WarnRatio = 0.9

	maxCostEUR = 1e6
	maxCount   = 1_000_000
)

type Level string

const (
	LevelOff      Level = "off"
	LevelOK       Level = "ok"
	LevelWarning  Level = "warning"
	LevelExceeded Level = "exceeded"
)

type Quota struct {
	MaxCostEURPerDay   float64 `json:"max_cost_eur_per_day"`
	MaxTasksPerDay     int     `json:"max_tasks_per_day"`
	MaxConcurrentTasks int     `json:"max_concurrent_tasks"`
}

type Usage struct {
	CostEUR         float64 `json:"cost_eur"`
	Tasks           int     `json:"tasks"`
	ConcurrentTasks int     `json:"concurrent_tasks"`
}

type Today struct {
	Usage
	Percent     float64   `json:"percent"`
	Level       Level     `json:"level"`
	DayStartsAt time.Time `json:"day_starts_at"`
	ResetsAt    time.Time `json:"resets_at"`
}

func (q Quota) Validate() error {
	if q.MaxCostEURPerDay < 0 || q.MaxCostEURPerDay > maxCostEUR || math.IsNaN(q.MaxCostEURPerDay) {
		return model.BadRequest("max_cost_eur_per_day must be a number between 0 and %g", float64(maxCostEUR))
	}
	if q.MaxTasksPerDay < 0 || q.MaxTasksPerDay > maxCount {
		return model.BadRequest("max_tasks_per_day must be an integer between 0 and %d", maxCount)
	}
	if q.MaxConcurrentTasks < 0 || q.MaxConcurrentTasks > maxCount {
		return model.BadRequest("max_concurrent_tasks must be an integer between 0 and %d", maxCount)
	}
	return nil
}

func (q Quota) Unlimited() bool { return q == Quota{} }

func DayStart(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func DayEnd(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

func Percent(q Quota, u Usage) float64 {
	var top float64
	if q.MaxCostEURPerDay > 0 {
		top = math.Max(top, u.CostEUR/q.MaxCostEURPerDay)
	}
	if q.MaxTasksPerDay > 0 {
		top = math.Max(top, float64(u.Tasks)/float64(q.MaxTasksPerDay))
	}
	if q.MaxConcurrentTasks > 0 {
		top = math.Max(top, float64(u.ConcurrentTasks)/float64(q.MaxConcurrentTasks))
	}
	return top * 100
}

func LevelOf(q Quota, u Usage) Level {
	if q.Unlimited() {
		return LevelOff
	}
	switch p := Percent(q, u) / 100; {
	case p >= 1:
		return LevelExceeded
	case p >= WarnRatio:
		return LevelWarning
	}
	return LevelOK
}

func Exceeded(q Quota, u Usage, newTask bool) (limit string, hit bool) {
	switch {
	case q.MaxCostEURPerDay > 0 && u.CostEUR >= q.MaxCostEURPerDay:
		return "max_cost_eur_per_day", true
	case newTask && q.MaxTasksPerDay > 0 && u.Tasks >= q.MaxTasksPerDay:
		return "max_tasks_per_day", true
	case q.MaxConcurrentTasks > 0 && u.ConcurrentTasks >= q.MaxConcurrentTasks:
		return "max_concurrent_tasks", true
	}
	return "", false
}

func Window(q Quota, u Usage, now time.Time) Today {
	return Today{Usage: u, Percent: Percent(q, u), Level: LevelOf(q, u), DayStartsAt: DayStart(now), ResetsAt: DayEnd(now)}
}
