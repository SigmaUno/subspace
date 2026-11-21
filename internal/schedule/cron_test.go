package schedule_test

import (
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/schedule"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCronNext(t *testing.T) {
	cases := []struct {
		name, cron, tz, after string
		want                  []string
	}{
		{"every quarter hour", "*/15 * * * *", "UTC", "2026-11-21T10:07:00Z", []string{"2026-11-21T10:15:00Z", "2026-11-21T10:30:00Z"}},
		{"strictly after", "*/15 * * * *", "UTC", "2026-11-21T10:15:00Z", []string{"2026-11-21T10:30:00Z"}},
		{"seconds are ignored", "0 * * * *", "UTC", "2026-11-21T10:00:30Z", []string{"2026-11-21T11:00:00Z"}},
		{"weekly monday", "0 9 * * 1", "UTC", "2026-11-21T10:00:00Z", []string{"2026-11-23T09:00:00Z", "2026-11-30T09:00:00Z"}},
		{"year rollover", "0 0 1 * *", "UTC", "2026-12-31T23:59:59Z", []string{"2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z"}},
		{"day of month or day of week", "30 4 1,15 * 5", "UTC", "2026-11-02T00:00:00Z", []string{"2026-11-06T04:30:00Z", "2026-11-13T04:30:00Z", "2026-11-15T04:30:00Z"}},
		{"star day of week requires day of month", "0 0 13 * *", "UTC", "2026-11-14T00:00:00Z", []string{"2026-12-13T00:00:00Z"}},
		{"leap day", "0 12 29 2 *", "UTC", "2026-01-01T00:00:00Z", []string{"2028-02-29T12:00:00Z", "2032-02-29T12:00:00Z"}},
		{"range with step and list", "10-40/10 8,10 * * *", "UTC", "2026-11-21T08:25:00Z", []string{"2026-11-21T08:30:00Z", "2026-11-21T08:40:00Z", "2026-11-21T10:10:00Z"}},
		{"open step", "5/20 * * * *", "UTC", "2026-11-21T10:00:00Z", []string{"2026-11-21T10:05:00Z", "2026-11-21T10:25:00Z", "2026-11-21T10:45:00Z", "2026-11-21T11:05:00Z"}},
		{"names", "0 8 * * mon-fri", "UTC", "2026-11-20T09:00:00Z", []string{"2026-11-23T08:00:00Z", "2026-11-24T08:00:00Z"}},
		{"month names", "0 0 1 Feb,JUN *", "UTC", "2026-11-21T00:00:00Z", []string{"2027-02-01T00:00:00Z", "2027-06-01T00:00:00Z"}},
		{"sunday as 7", "0 0 * * 7", "UTC", "2026-11-21T10:00:00Z", []string{"2026-11-22T00:00:00Z", "2026-11-29T00:00:00Z"}},
		{"sunday range through 7", "0 0 * * 5-7", "UTC", "2026-11-21T10:00:00Z", []string{"2026-11-22T00:00:00Z", "2026-11-27T00:00:00Z"}},
		{"half hour zone", "0 9 * * *", "Asia/Kolkata", "2026-11-21T00:00:00Z", []string{"2026-11-21T03:30:00Z", "2026-11-22T03:30:00Z"}},
		{"new york gap runs when the clocks jump", "30 2 * * *", "America/New_York", "2026-03-07T12:00:00Z", []string{"2026-03-08T07:00:00Z", "2026-03-09T06:30:00Z"}},
		{"new york gap fires once", "15,45 2 * * *", "America/New_York", "2026-03-07T12:00:00Z", []string{"2026-03-08T07:00:00Z", "2026-03-09T06:15:00Z"}},
		{"new york overlap fires at the first occurrence", "30 1 * * *", "America/New_York", "2026-10-31T12:00:00Z", []string{"2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"}},
		{"new york hourly across the overlap", "0 * * * *", "America/New_York", "2026-11-01T04:00:00Z", []string{"2026-11-01T05:00:00Z", "2026-11-01T07:00:00Z"}},
		{"berlin gap", "30 2 * * *", "Europe/Berlin", "2026-03-28T12:00:00Z", []string{"2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z"}},
		{"berlin overlap", "30 2 * * *", "Europe/Berlin", "2026-10-24T12:00:00Z", []string{"2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"}},
		{"berlin midnight over spring forward", "0 0 * * *", "Europe/Berlin", "2026-03-28T23:00:00Z", []string{"2026-03-29T22:00:00Z", "2026-03-30T22:00:00Z"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, err := schedule.ParseSpec(c.cron, c.tz)
			if err != nil {
				t.Fatal(err)
			}
			from := at(t, c.after)
			for _, want := range c.want {
				got, ok := spec.Next(from)
				if !ok || !got.Equal(at(t, want)) {
					t.Fatalf("after %s: got %s (%v), want %s", from.Format(time.RFC3339), got.UTC().Format(time.RFC3339), ok, want)
				}
				from = got
			}
		})
	}
}

func TestCronRejectsInvalidExpressions(t *testing.T) {
	cases := []struct{ name, cron, tz, want string }{
		{"empty", "", "UTC", "5 fields"},
		{"too few fields", "* * * *", "UTC", "5 fields"},
		{"too many fields", "* * * * * *", "UTC", "5 fields"},
		{"macro", "@daily", "UTC", "5 fields"},
		{"minute out of range", "60 * * * *", "UTC", "out of range"},
		{"hour out of range", "* 24 * * *", "UTC", "out of range"},
		{"day zero", "* * 0 * *", "UTC", "out of range"},
		{"day 32", "* * 32 * *", "UTC", "out of range"},
		{"month 13", "* * * 13 *", "UTC", "out of range"},
		{"weekday 8", "* * * * 8", "UTC", "out of range"},
		{"negative", "-1 * * * *", "UTC", "invalid value"},
		{"zero step", "*/0 * * * *", "UTC", "invalid step"},
		{"word step", "*/x * * * *", "UTC", "invalid step"},
		{"reversed range", "5-1 * * * *", "UTC", "reversed"},
		{"open range", "1- * * * *", "UTC", "invalid value"},
		{"word", "a * * * *", "UTC", "invalid value"},
		{"empty list item", "1,,2 * * * *", "UTC", "empty list item"},
		{"never matches", "0 0 31 2 *", "UTC", "never matches"},
		{"unknown zone", "* * * * *", "Mars/Olympus", "unknown timezone"},
		{"local zone", "* * * * *", "Local", "IANA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := schedule.ParseSpec(c.cron, c.tz)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}
