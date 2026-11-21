package quota

import (
	"testing"
	"time"
)

func TestWindowRollover(t *testing.T) {
	zone := time.FixedZone("UTC+14", 14*3600)
	cases := []struct {
		name       string
		now        time.Time
		start, end string
	}{
		{"mid day", time.Date(2026, 11, 21, 10, 30, 0, 0, time.UTC), "2026-11-21T00:00:00Z", "2026-11-22T00:00:00Z"},
		{"first instant", time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), "2026-11-21T00:00:00Z", "2026-11-22T00:00:00Z"},
		{"last instant", time.Date(2026, 11, 21, 23, 59, 59, 999999999, time.UTC), "2026-11-21T00:00:00Z", "2026-11-22T00:00:00Z"},
		{"month end", time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC), "2026-11-30T00:00:00Z", "2026-12-01T00:00:00Z"},
		{"year end", time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), "2026-12-31T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"leap day", time.Date(2028, 2, 29, 1, 0, 0, 0, time.UTC), "2028-02-29T00:00:00Z", "2028-03-01T00:00:00Z"},
		{"other zone is read as UTC", time.Date(2026, 11, 22, 3, 0, 0, 0, zone), "2026-11-21T00:00:00Z", "2026-11-22T00:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DayStart(c.now).Format(time.RFC3339); got != c.start {
				t.Errorf("start %s, want %s", got, c.start)
			}
			if got := DayEnd(c.now).Format(time.RFC3339); got != c.end {
				t.Errorf("end %s, want %s", got, c.end)
			}
		})
	}
}

func TestWindowLevels(t *testing.T) {
	q := Quota{MaxCostEURPerDay: 10, MaxTasksPerDay: 4, MaxConcurrentTasks: 2}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		quota Quota
		usage Usage
		level Level
		pct   float64
	}{
		{"no limits", Quota{}, Usage{CostEUR: 99, Tasks: 9, ConcurrentTasks: 9}, LevelOff, 0},
		{"idle", q, Usage{}, LevelOK, 0},
		{"just under 90", q, Usage{CostEUR: 8.99}, LevelOK, 89.9},
		{"at 90", q, Usage{CostEUR: 9}, LevelWarning, 90},
		{"tasks drive it", q, Usage{Tasks: 4}, LevelExceeded, 100},
		{"concurrency drives it", q, Usage{ConcurrentTasks: 2}, LevelExceeded, 100},
		{"unlimited dimension ignored", Quota{MaxTasksPerDay: 10}, Usage{CostEUR: 1000, Tasks: 9}, LevelWarning, 90},
		{"sub-euro cost limit", Quota{MaxCostEURPerDay: 0.5}, Usage{CostEUR: 0.5}, LevelExceeded, 100},
		{"single concurrent slot", Quota{MaxConcurrentTasks: 1}, Usage{ConcurrentTasks: 1}, LevelExceeded, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Window(c.quota, c.usage, now)
			if w.Level != c.level {
				t.Errorf("level %s, want %s", w.Level, c.level)
			}
			if d := w.Percent - c.pct; d > 0.01 || d < -0.01 {
				t.Errorf("percent %.2f, want %.2f", w.Percent, c.pct)
			}
			if !w.ResetsAt.Equal(DayEnd(now)) || !w.DayStartsAt.Equal(DayStart(now)) {
				t.Errorf("window %s..%s", w.DayStartsAt, w.ResetsAt)
			}
		})
	}
}

func TestExceeded(t *testing.T) {
	q := Quota{MaxCostEURPerDay: 5, MaxTasksPerDay: 2, MaxConcurrentTasks: 1}
	cases := []struct {
		name    string
		usage   Usage
		newTask bool
		limit   string
	}{
		{"within limits", Usage{CostEUR: 4.99, Tasks: 1}, true, ""},
		{"cost cap", Usage{CostEUR: 5}, true, "max_cost_eur_per_day"},
		{"task cap", Usage{Tasks: 2}, true, "max_tasks_per_day"},
		{"task cap ignores an already counted task", Usage{Tasks: 2}, false, ""},
		{"concurrency cap", Usage{ConcurrentTasks: 1}, false, "max_concurrent_tasks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			limit, hit := Exceeded(q, c.usage, c.newTask)
			if limit != c.limit || hit != (c.limit != "") {
				t.Errorf("got %q %v, want %q", limit, hit, c.limit)
			}
		})
	}
	if limit, _ := Exceeded(Quota{MaxCostEURPerDay: 0.5}, Usage{CostEUR: 0.6}, true); limit != "max_cost_eur_per_day" {
		t.Errorf("sub-euro cost cap: %q", limit)
	}
	if _, hit := Exceeded(Quota{}, Usage{CostEUR: 1e9, Tasks: 1e9, ConcurrentTasks: 1e9}, true); hit {
		t.Error("zero quota must be unlimited")
	}
}
