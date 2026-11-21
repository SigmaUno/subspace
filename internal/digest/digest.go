package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/redact"
)

const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"

	SourceSpend    = "spend"
	SourceForecast = "forecast"

	maxTitles   = 5
	maxIDs      = 10
	maxTitleLen = 80
)

var severityRank = map[string]int{SeverityHigh: 0, SeverityMedium: 1}

type Risk struct {
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Message  string `json:"message"`
}

type Pending struct {
	ID       string
	Title    string
	Priority int
}

type Snapshot struct {
	Since      time.Time
	Until      time.Time
	Done       int
	Created    int
	Blocked    int
	CostEUR    float64
	Tokens     int64
	Risks      []Risk
	Decisions  []Pending
	Flapping   []string
	MergeReady []string
}

type Tasks struct {
	Done    int `json:"done"`
	Created int `json:"created"`
	Blocked int `json:"blocked"`
}

type Decisions struct {
	Count  int      `json:"count"`
	Titles []string `json:"titles"`
}

type Digest struct {
	Since      time.Time `json:"since"`
	Until      time.Time `json:"until"`
	Tasks      Tasks     `json:"tasks"`
	CostEUR    float64   `json:"cost_eur"`
	Tokens     int64     `json:"tokens"`
	Risks      []Risk    `json:"risks"`
	Decisions  Decisions `json:"decisions"`
	Flapping   []string  `json:"flapping_agents"`
	MergeReady []string  `json:"merge_ready"`

	state string
}

func Build(s Snapshot) Digest {
	d := Digest{
		Since:      s.Since.UTC(),
		Until:      s.Until.UTC(),
		Tasks:      Tasks{Done: s.Done, Created: s.Created, Blocked: s.Blocked},
		CostEUR:    s.CostEUR,
		Tokens:     s.Tokens,
		Risks:      slices.Clone(s.Risks),
		Flapping:   sortedUnique(s.Flapping),
		MergeReady: sortedUnique(s.MergeReady),
		Decisions:  Decisions{Count: len(s.Decisions), Titles: []string{}},
	}
	slices.SortFunc(d.Risks, func(a, b Risk) int {
		if c := severityRank[a.Severity] - severityRank[b.Severity]; c != 0 {
			return c
		}
		return strings.Compare(a.Source+"/"+a.Subject, b.Source+"/"+b.Subject)
	})
	if d.Risks == nil {
		d.Risks = []Risk{}
	}
	pending := slices.Clone(s.Decisions)
	slices.SortFunc(pending, func(a, b Pending) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, p := range pending[:min(len(pending), maxTitles)] {
		d.Decisions.Titles = append(d.Decisions.Titles, clean(p.Title))
	}
	d.state = fingerprint(d, pending)
	return d
}

func (d Digest) Active() bool {
	return d.Tasks.Done+d.Tasks.Created+d.Tasks.Blocked > 0 || d.CostEUR > 0 || d.Tokens > 0
}

func (d Digest) Fingerprint() string { return d.state }

func (d Digest) ShouldSend(last string) bool { return d.Active() || d.state != last }

func (d Digest) Title() string {
	return fmt.Sprintf("Subspace digest: %d done, %d decisions pending", d.Tasks.Done, d.Decisions.Count)
}

func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Window: %s to %s UTC\n", d.Since.Format("2006-01-02 15:04"), d.Until.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Tasks: %d done, %d created, %d blocked\n", d.Tasks.Done, d.Tasks.Created, d.Tasks.Blocked)
	fmt.Fprintf(&b, "Spend: EUR %.2f, %d tokens\n", d.CostEUR, d.Tokens)
	if len(d.Risks) > 0 {
		b.WriteString("Risks:\n")
		for _, r := range d.Risks {
			fmt.Fprintf(&b, "- [%s] %s %s: %s\n", r.Severity, r.Source, r.Subject, r.Message)
		}
	}
	if d.Decisions.Count > 0 {
		fmt.Fprintf(&b, "Decisions pending: %d\n", d.Decisions.Count)
		for _, t := range d.Decisions.Titles {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		if more := d.Decisions.Count - len(d.Decisions.Titles); more > 0 {
			fmt.Fprintf(&b, "- and %d more\n", more)
		}
	}
	list(&b, "Flapping agents", d.Flapping)
	list(&b, "Merge-ready PRs", d.MergeReady)
	return strings.TrimRight(b.String(), "\n")
}

func (d Digest) Notification(id string) notify.Notification {
	return notify.Notification{
		ID: id, Kind: "digest", Title: d.Title(), Text: d.Text(), Priority: 3, TS: d.Until,
	}
}

func list(b *strings.Builder, label string, ids []string) {
	if len(ids) == 0 {
		return
	}
	shown := ids[:min(len(ids), maxIDs)]
	fmt.Fprintf(b, "%s: %s", label, strings.Join(shown, ", "))
	if more := len(ids) - len(shown); more > 0 {
		fmt.Fprintf(b, " and %d more", more)
	}
	b.WriteByte('\n')
}

func fingerprint(d Digest, pending []Pending) string {
	if len(pending) == 0 && len(d.Risks) == 0 && len(d.Flapping) == 0 && len(d.MergeReady) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range pending {
		fmt.Fprintf(&b, "d\x00%s\x00%s\n", p.ID, p.Title)
	}
	for _, r := range d.Risks {
		fmt.Fprintf(&b, "r\x00%s\x00%s\x00%s\n", r.Source, r.Subject, r.Severity)
	}
	for _, id := range d.Flapping {
		fmt.Fprintf(&b, "f\x00%s\n", id)
	}
	for _, id := range d.MergeReady {
		fmt.Fprintf(&b, "m\x00%s\n", id)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func clean(title string) string {
	s := strings.Join(strings.Fields(redact.String(title)), " ")
	r := []rune(s)
	if len(r) <= maxTitleLen {
		return s
	}
	return string(r[:maxTitleLen-3]) + "..."
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		return []string{}
	}
	return out
}
