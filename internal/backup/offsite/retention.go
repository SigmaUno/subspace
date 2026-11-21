package offsite

import (
	"fmt"
	"sort"
	"time"
)

type Policy struct {
	Last   int
	Daily  int
	Weekly int
}

var DefaultPolicy = Policy{Last: 7, Daily: 7, Weekly: 4}

type Entry struct {
	Name    string
	Created time.Time
	Size    int64
}

func (p Policy) Prune(entries []Entry, protect string) []Entry {
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].Created.Equal(sorted[j].Created) {
			return sorted[i].Created.After(sorted[j].Created)
		}
		return sorted[i].Name > sorted[j].Name
	})
	keep := map[string]bool{protect: protect != ""}
	for i := 0; i < len(sorted) && i < p.Last; i++ {
		keep[sorted[i].Name] = true
	}
	bucket := func(limit int, key func(time.Time) string) {
		seen := map[string]bool{}
		for _, e := range sorted {
			if len(seen) >= limit {
				return
			}
			k := key(e.Created.UTC())
			if seen[k] {
				continue
			}
			seen[k] = true
			keep[e.Name] = true
		}
	}
	bucket(p.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(p.Weekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	var drop []Entry
	for _, e := range sorted {
		if !keep[e.Name] {
			drop = append(drop, e)
		}
	}
	return drop
}
