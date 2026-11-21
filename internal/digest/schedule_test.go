package digest

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata for %s unavailable: %v", name, err)
	}
	return loc
}

func daily(loc *time.Location, times ...string) Rule {
	r := Rule{Name: RuleDaily, Loc: loc}
	for _, v := range times {
		c, _ := time.Parse("15:04", v)
		r.Slots = append(r.Slots, Slot{Day: anyDay, At: time.Duration(c.Hour())*time.Hour + time.Duration(c.Minute())*time.Minute})
	}
	return r
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestRuleNextAndLatest(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	berlin := mustLoc(t, "Europe/Berlin")
	cases := []struct {
		name       string
		rule       Rule
		at         time.Time
		wantNext   time.Time
		wantLatest time.Time
	}{
		{
			name:     "utc between slots",
			rule:     daily(time.UTC, "08:00", "18:00"),
			at:       utc(2026, 11, 21, 12, 0),
			wantNext: utc(2026, 11, 21, 18, 0), wantLatest: utc(2026, 11, 21, 8, 0),
		},
		{
			name:     "exactly on a slot belongs to latest",
			rule:     daily(time.UTC, "08:00", "18:00"),
			at:       utc(2026, 11, 21, 18, 0),
			wantNext: utc(2026, 11, 22, 8, 0), wantLatest: utc(2026, 11, 21, 18, 0),
		},
		{
			name:     "after the last slot of the day",
			rule:     daily(time.UTC, "08:00", "18:00"),
			at:       utc(2026, 11, 21, 23, 59),
			wantNext: utc(2026, 11, 22, 8, 0), wantLatest: utc(2026, 11, 21, 18, 0),
		},
		{
			name:     "fixed offset zone",
			rule:     daily(time.FixedZone("x", 5*3600+1800), "08:00"),
			at:       utc(2026, 11, 21, 0, 0),
			wantNext: utc(2026, 11, 21, 2, 30), wantLatest: utc(2026, 11, 20, 2, 30),
		},
		{
			name:     "new york winter",
			rule:     daily(ny, "08:00"),
			at:       utc(2026, 1, 15, 0, 0),
			wantNext: utc(2026, 1, 15, 13, 0), wantLatest: utc(2026, 1, 14, 13, 0),
		},
		{
			name:     "new york summer",
			rule:     daily(ny, "08:00"),
			at:       utc(2026, 7, 15, 0, 0),
			wantNext: utc(2026, 7, 15, 12, 0), wantLatest: utc(2026, 7, 14, 12, 0),
		},
		{
			name:     "spring forward: nonexistent 02:30 fires right after the gap",
			rule:     daily(ny, "02:30"),
			at:       utc(2026, 3, 8, 0, 0),
			wantNext: utc(2026, 3, 8, 7, 30), wantLatest: utc(2026, 3, 7, 7, 30),
		},
		{
			name:     "spring forward: slot already due after the gap",
			rule:     daily(ny, "02:30"),
			at:       utc(2026, 3, 8, 7, 30),
			wantNext: utc(2026, 3, 9, 6, 30), wantLatest: utc(2026, 3, 8, 7, 30),
		},
		{
			name:     "spring forward: the day after keeps wall clock",
			rule:     daily(ny, "08:00"),
			at:       utc(2026, 3, 8, 12, 0),
			wantNext: utc(2026, 3, 9, 12, 0), wantLatest: utc(2026, 3, 8, 12, 0),
		},
		{
			name:     "fall back: ambiguous 01:30 fires once at the first occurrence",
			rule:     daily(ny, "01:30"),
			at:       utc(2026, 11, 1, 5, 30),
			wantNext: utc(2026, 11, 2, 6, 30), wantLatest: utc(2026, 11, 1, 5, 30),
		},
		{
			name:     "fall back: second occurrence of 01:30 does not fire again",
			rule:     daily(ny, "01:30"),
			at:       utc(2026, 11, 1, 6, 30),
			wantNext: utc(2026, 11, 2, 6, 30), wantLatest: utc(2026, 11, 1, 5, 30),
		},
		{
			name:     "fall back: day is 25 hours long",
			rule:     daily(ny, "08:00"),
			at:       utc(2026, 11, 1, 13, 0),
			wantNext: utc(2026, 11, 2, 13, 0), wantLatest: utc(2026, 11, 1, 13, 0),
		},
		{
			name:     "berlin spring forward 02:30",
			rule:     daily(berlin, "02:30"),
			at:       utc(2026, 3, 29, 0, 0),
			wantNext: utc(2026, 3, 29, 1, 30), wantLatest: utc(2026, 3, 28, 1, 30),
		},
		{
			name:     "slots inside the gap collapse onto the same instant",
			rule:     daily(ny, "02:00", "03:00"),
			at:       utc(2026, 3, 8, 6, 0),
			wantNext: utc(2026, 3, 8, 7, 0), wantLatest: utc(2026, 3, 7, 8, 0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next, ok := c.rule.Next(c.at)
			if !ok || !next.Equal(c.wantNext) {
				t.Errorf("Next = %v, want %v", next.UTC(), c.wantNext)
			}
			latest, ok := c.rule.Latest(c.at)
			if !ok || !latest.Equal(c.wantLatest) {
				t.Errorf("Latest = %v, want %v", latest.UTC(), c.wantLatest)
			}
		})
	}
}

func TestRuleSlotsInGapAreDistinctFromNextDay(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	r := daily(ny, "02:00", "03:00")
	var got []time.Time
	for at := utc(2026, 3, 8, 6, 0); len(got) < 3; {
		next, _ := r.Next(at)
		got = append(got, next)
		at = next
	}
	want := []time.Time{utc(2026, 3, 8, 7, 0), utc(2026, 3, 9, 6, 0), utc(2026, 3, 9, 7, 0)}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("slot %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestWeeklyRule(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	cfg, err := ConfigFromEnv(env{EnvWeekly: "mon@09:00, Friday@17:30", EnvTZ: "America/New_York"}.get)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Rules[0]
	if r.Name != RuleWeekly || r.Loc.String() != ny.String() {
		t.Fatalf("rule: %+v", r)
	}
	cases := []struct {
		at, next, latest time.Time
	}{
		{utc(2026, 11, 21, 12, 0), utc(2026, 11, 23, 14, 0), utc(2026, 11, 20, 22, 30)},
		{utc(2026, 11, 23, 14, 0), utc(2026, 11, 27, 22, 30), utc(2026, 11, 23, 14, 0)},
		{utc(2026, 11, 1, 12, 0), utc(2026, 11, 2, 14, 0), utc(2026, 10, 30, 21, 30)},
	}
	for _, c := range cases {
		next, _ := r.Next(c.at)
		latest, _ := r.Latest(c.at)
		if !next.Equal(c.next) || !latest.Equal(c.latest) {
			t.Errorf("at %v: next %v latest %v, want %v %v", c.at, next.UTC(), latest.UTC(), c.next, c.latest)
		}
	}
}

type env map[string]string

func (e env) get(k string) string { return e[k] }

func TestConfigFromEnv(t *testing.T) {
	cfg, err := ConfigFromEnv(env{}.get)
	if err != nil || len(cfg.Rules) != 0 {
		t.Fatalf("empty env: %+v %v", cfg, err)
	}
	cfg, err = ConfigFromEnv(env{EnvAt: "08:00,18:00"}.get)
	if err != nil || len(cfg.Rules) != 1 || cfg.Rules[0].Loc != time.UTC || len(cfg.Rules[0].Slots) != 2 {
		t.Fatalf("daily: %+v %v", cfg, err)
	}
	bad := map[string]env{
		"hour out of range": {EnvAt: "25:00"},
		"missing minutes":   {EnvAt: "8"},
		"empty item":        {EnvAt: "08:00,"},
		"unknown zone":      {EnvAt: "08:00", EnvTZ: "Mars/Olympus"},
		"weekly no day":     {EnvWeekly: "09:00"},
		"weekly bad day":    {EnvWeekly: "someday@09:00"},
		"weekly abbrev":     {EnvWeekly: "mo@09:00"},
	}
	for name, e := range bad {
		if _, err := ConfigFromEnv(e.get); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if !strings.Contains(err.Error(), "SUBSPACE_DIGEST_") {
			t.Errorf("%s: error does not name the variable: %v", name, err)
		}
	}
}
