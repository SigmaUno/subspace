package report

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func Markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", r.BriefID, r.Title)
	fmt.Fprintf(&b, "Generated %s\n\n", r.GeneratedAt.Format(time.RFC3339))

	b.WriteString("## Status\n\n| Status | Tasks |\n|---|--:|\n")
	for _, s := range model.AllStatuses {
		if n := r.Counts[s]; n > 0 {
			fmt.Fprintf(&b, "| %s | %d |\n", s, n)
		}
	}
	fmt.Fprintf(&b, "| **total** | **%d** |\n\n", r.Total)

	b.WriteString("## Tasks\n\n| Task | Title | Status | Agent | Queue | Work | Review | Blocked | Rounds | Cost € |\n|---|---|---|---|--:|--:|--:|--:|--:|--:|\n")
	for _, t := range r.Tasks {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %d | %.2f |\n",
			t.ID, cell(t.Title), t.Status, agentKey(t.Assignee),
			duration(t.QueueSeconds), duration(t.WorkSeconds), duration(t.ReviewSeconds), duration(t.BlockedSeconds),
			t.ReviewRounds, t.CostEUR)
	}
	b.WriteString("\n")

	b.WriteString("## Timelines\n\n")
	for _, t := range r.Tasks {
		fmt.Fprintf(&b, "### %s %s\n\n", t.ID, t.Title)
		if len(t.Timeline) == 0 {
			b.WriteString("No recorded transitions.\n\n")
			continue
		}
		for _, s := range t.Timeline {
			fmt.Fprintf(&b, "- %s `%s → %s` by %s via %s (+%s)\n", s.At.Format(time.RFC3339), s.From, s.To, s.By, s.Tool, duration(s.ElapsedSeconds))
		}
		for i, rv := range t.Reviews {
			fmt.Fprintf(&b, "- review %d: %s by %s after %s\n", i+1, rv.Verdict, rv.Reviewer, duration(rv.WaitSeconds))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Cost\n\nTotal: %.2f €\n\n", r.Cost.TotalEUR)
	costTable(&b, "Agent", r.Cost.ByAgent)
	costTable(&b, "Provider", r.Cost.ByProvider)

	fmt.Fprintf(&b, "## Flow\n\n- Blocked time: %s\n- Done: %d in %.2f h (%.2f per hour)\n", duration(r.BlockedSeconds), r.Throughput.Done, r.Throughput.WindowHours, r.Throughput.PerHour)
	if len(r.CriticalPath.TaskIDs) > 0 {
		fmt.Fprintf(&b, "- Critical path: %s (%s)\n", strings.Join(r.CriticalPath.TaskIDs, " → "), duration(r.CriticalPath.Seconds))
	}
	b.WriteString("\n")

	b.WriteString("## Outliers\n\n")
	if len(r.Outliers) == 0 {
		b.WriteString("None.\n")
	}
	for _, o := range r.Outliers {
		fmt.Fprintf(&b, "- %s %s: %g (threshold %g)\n", o.TaskID, o.Metric, o.Value, o.Threshold)
	}
	return b.String()
}

func costTable(b *strings.Builder, label string, m map[string]float64) {
	fmt.Fprintf(b, "| %s | Cost € |\n|---|--:|\n", label)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		fmt.Fprintf(b, "| %s | %.2f |\n", k, m[k])
	}
	b.WriteString("\n")
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func duration(sec int64) string {
	d := time.Duration(sec) * time.Second
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
