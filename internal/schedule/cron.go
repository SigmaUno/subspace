package schedule

import (
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxScanDays = 366 * 9
	maxOffset   = 15 * time.Hour
)

type field struct {
	name     string
	min, max int
	names    []string
	offset   int
}

var (
	fMinute = field{name: "minute", min: 0, max: 59}
	fHour   = field{name: "hour", min: 0, max: 23}
	fDom    = field{name: "day of month", min: 1, max: 31}
	fMonth  = field{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}, offset: 1}
	fDow    = field{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}}
)

type Cron struct {
	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
	loc     *time.Location
}

func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "utc") {
		return time.UTC, nil
	}
	if strings.EqualFold(name, "local") {
		return nil, model.BadRequest("timezone must be an IANA name such as Europe/Berlin")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, model.BadRequest("unknown timezone %q", name)
	}
	return loc, nil
}

func ParseCron(expr string, loc *time.Location) (*Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, model.BadRequest("cron needs 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	if loc == nil {
		loc = time.UTC
	}
	c := &Cron{loc: loc, domStar: strings.HasPrefix(parts[2], "*"), dowStar: strings.HasPrefix(parts[4], "*")}
	fields := []field{fMinute, fHour, fDom, fMonth, fDow}
	for i, dst := range []*uint64{&c.minute, &c.hour, &c.dom, &c.month, &c.dow} {
		set, err := parseField(parts[i], fields[i])
		if err != nil {
			return nil, err
		}
		*dst = set
	}
	if c.dow&(1<<7) != 0 {
		c.dow = c.dow&^(1<<7) | 1
	}
	if !c.domStar && c.dowStar && !c.anyDate() {
		return nil, model.BadRequest("cron never matches: no selected month has that day of month")
	}
	return c, nil
}

func (c *Cron) anyDate() bool {
	for m := 1; m <= 12; m++ {
		if c.month&(1<<m) == 0 {
			continue
		}
		days := time.Date(2024, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for d := 1; d <= days; d++ {
			if c.dom&(1<<d) != 0 {
				return true
			}
		}
	}
	return false
}

func parseField(s string, f field) (uint64, error) {
	var set uint64
	for item := range strings.SplitSeq(s, ",") {
		bits, err := parseItem(item, f)
		if err != nil {
			return 0, err
		}
		set |= bits
	}
	return set, nil
}

func parseItem(item string, f field) (uint64, error) {
	if item == "" {
		return 0, model.BadRequest("cron %s: empty list item", f.name)
	}
	rng, stepText, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return 0, model.BadRequest("cron %s: invalid step %q", f.name, stepText)
		}
		step = n
	}
	lo, hi := f.min, f.max
	from, to, isRange := strings.Cut(rng, "-")
	switch {
	case rng == "*":
	case isRange:
		var err error
		if lo, err = f.value(from); err != nil {
			return 0, err
		}
		if hi, err = f.value(to); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, model.BadRequest("cron %s: range %q is reversed", f.name, rng)
		}
	default:
		v, err := f.value(rng)
		if err != nil {
			return 0, err
		}
		lo = v
		if !hasStep {
			hi = v
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << v
	}
	return bits, nil
}

func (f field) value(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < f.min || n > f.max {
			return 0, model.BadRequest("cron %s: value %d out of range %d-%d", f.name, n, f.min, f.max)
		}
		return n, nil
	}
	for i, name := range f.names {
		if strings.EqualFold(s, name) {
			return i + f.offset, nil
		}
	}
	return 0, model.BadRequest("cron %s: invalid value %q", f.name, s)
}

func (c *Cron) Next(after time.Time) (time.Time, bool) {
	y, m, d := after.In(c.loc).Date()
	for i := -1; i < maxScanDays; i++ {
		noon := time.Date(y, m, d+i, 12, 0, 0, 0, c.loc)
		dy, dm, dd := noon.Date()
		if !c.matchesDay(dm, dd, noon.Weekday()) {
			continue
		}
		for h := 0; h < 24; h++ {
			if c.hour&(1<<h) == 0 || !time.Date(dy, dm, dd, h, 59, 0, 0, time.UTC).Add(maxOffset).After(after) {
				continue
			}
			for mi := 0; mi < 60; mi++ {
				if c.minute&(1<<mi) == 0 {
					continue
				}
				if t := resolve(dy, dm, dd, h, mi, c.loc); t.After(after) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

func (c *Cron) matchesDay(m time.Month, d int, wd time.Weekday) bool {
	if c.month&(1<<int(m)) == 0 {
		return false
	}
	dom := c.dom&(1<<d) != 0
	dow := c.dow&(1<<int(wd)) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}

func resolve(y int, mo time.Month, d, h, mi int, loc *time.Location) time.Time {
	naive := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	var best, before time.Time
	for i, probe := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, off := naive.Add(probe).In(loc).Zone()
		c := naive.Add(-time.Duration(off) * time.Second)
		if i == 0 {
			before = c
		}
		l := c.In(loc)
		if l.Year() == y && l.Month() == mo && l.Day() == d && l.Hour() == h && l.Minute() == mi && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	start, _ := before.In(loc).ZoneBounds()
	return start
}
