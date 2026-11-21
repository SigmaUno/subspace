package digest

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	EnvAt     = "SUBSPACE_DIGEST_AT"
	EnvWeekly = "SUBSPACE_DIGEST_WEEKLY"
	EnvTZ     = "SUBSPACE_DIGEST_TZ"

	RuleDaily  = "daily"
	RuleWeekly = "weekly"

	anyDay = -1
	span   = 9
)

type Slot struct {
	Day int
	At  time.Duration
}

type Rule struct {
	Name  string
	Loc   *time.Location
	Slots []Slot
}

type Config struct {
	Rules []Rule
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	loc := time.UTC
	if name := strings.TrimSpace(getenv(EnvTZ)); name != "" {
		l, err := time.LoadLocation(name)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvTZ, err)
		}
		loc = l
	}
	var cfg Config
	if v := strings.TrimSpace(getenv(EnvAt)); v != "" {
		slots, err := parseSlots(v, false)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvAt, err)
		}
		cfg.Rules = append(cfg.Rules, Rule{Name: RuleDaily, Loc: loc, Slots: slots})
	}
	if v := strings.TrimSpace(getenv(EnvWeekly)); v != "" {
		slots, err := parseSlots(v, true)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvWeekly, err)
		}
		cfg.Rules = append(cfg.Rules, Rule{Name: RuleWeekly, Loc: loc, Slots: slots})
	}
	return cfg, nil
}

func parseSlots(v string, weekly bool) ([]Slot, error) {
	var out []Slot
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		day := anyDay
		clock := item
		if weekly {
			name, rest, ok := strings.Cut(item, "@")
			if !ok {
				return nil, fmt.Errorf("%q must look like mon@09:00", item)
			}
			wd, ok := parseWeekday(name)
			if !ok {
				return nil, fmt.Errorf("%q: unknown weekday", name)
			}
			day, clock = int(wd), strings.TrimSpace(rest)
		}
		t, err := time.Parse("15:04", clock)
		if err != nil {
			return nil, fmt.Errorf("%q must be HH:MM", clock)
		}
		out = append(out, Slot{Day: day, At: time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute})
	}
	return out, nil
}

func parseWeekday(name string) (time.Weekday, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for d := time.Sunday; d <= time.Saturday; d++ {
		full := strings.ToLower(d.String())
		if name == full || name == full[:3] {
			return d, true
		}
	}
	return 0, false
}

func (r Rule) Next(after time.Time) (time.Time, bool) {
	for _, s := range r.around(after) {
		if s.After(after) {
			return s, true
		}
	}
	return time.Time{}, false
}

func (r Rule) Latest(at time.Time) (time.Time, bool) {
	var out time.Time
	for _, s := range r.around(at) {
		if s.After(at) {
			break
		}
		out = s
	}
	return out, !out.IsZero()
}

func (r Rule) around(t time.Time) []time.Time {
	y, m, d := t.In(r.Loc).Date()
	var out []time.Time
	for i := -span; i <= span; i++ {
		noon := time.Date(y, m, d+i, 12, 0, 0, 0, r.Loc)
		dy, dm, dd := noon.Date()
		for _, s := range r.Slots {
			if s.Day == anyDay || s.Day == int(noon.Weekday()) {
				out = append(out, wall(dy, dm, dd, s.At, r.Loc))
			}
		}
	}
	slices.SortFunc(out, time.Time.Compare)
	return slices.CompactFunc(out, time.Time.Equal)
}

func wall(y int, m time.Month, d int, at time.Duration, loc *time.Location) time.Time {
	w := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Add(at)
	want := w.Format(time.DateTime)
	var best time.Time
	for _, probe := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, off := w.Add(probe).In(loc).Zone()
		c := w.Add(-time.Duration(off) * time.Second)
		if c.In(loc).Format(time.DateTime) == want && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	_, before := w.Add(-24 * time.Hour).In(loc).Zone()
	return w.Add(-time.Duration(before) * time.Second)
}
