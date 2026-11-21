package digest

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
)

func fixture() Snapshot {
	return Snapshot{
		Since: t0, Until: t1, Done: 3, Created: 5, Blocked: 1, CostEUR: 12.5, Tokens: 53000,
		Risks: []Risk{
			{Source: SourceForecast, Severity: SeverityMedium, Subject: "B-2", Message: "projected cost may exceed max_cost_eur"},
			{Source: SourceSpend, Severity: SeverityHigh, Subject: "daily", Message: "daily spend at 100% of its cap"},
		},
		Decisions: []Pending{
			{ID: "q:Q-1", Title: "Which database?", Priority: 70},
			{ID: "plan:P-1", Title: "Approve plan P-1 for B-1", Priority: 80},
			{ID: "unblock:T-4", Title: "Unblock T-4: flaky test", Priority: 50},
			{ID: "merge:T-5", Title: "Merge T-5: add login", Priority: 60},
			{ID: "stale:w1", Title: "Agent w1 is stale on T-6", Priority: 40},
			{ID: "token:w2", Title: "Token of w2 expires soon", Priority: 55},
			{ID: "q:Q-2", Title: "Second question", Priority: 70},
		},
		Flapping:   []string{"w2", "w1", "w2"},
		MergeReady: []string{"T-9", "T-2"},
	}
}

func TestBuildText(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want string
	}{
		{
			name: "quiet",
			snap: Snapshot{Since: t0, Until: t1},
			want: "Window: 2026-11-21 08:00 to 2026-11-21 18:00 UTC\nTasks: 0 done, 0 created, 0 blocked\nSpend: EUR 0.00, 0 tokens",
		},
		{
			name: "full",
			snap: fixture(),
			want: strings.Join([]string{
				"Window: 2026-11-21 08:00 to 2026-11-21 18:00 UTC",
				"Tasks: 3 done, 5 created, 1 blocked",
				"Spend: EUR 12.50, 53000 tokens",
				"Risks:",
				"- [high] spend daily: daily spend at 100% of its cap",
				"- [medium] forecast B-2: projected cost may exceed max_cost_eur",
				"Decisions pending: 7",
				"- Approve plan P-1 for B-1",
				"- Which database?",
				"- Second question",
				"- Merge T-5: add login",
				"- Token of w2 expires soon",
				"- and 2 more",
				"Flapping agents: w1, w2",
				"Merge-ready PRs: T-2, T-9",
			}, "\n"),
		},
		{
			name: "many merge-ready",
			snap: Snapshot{Since: t0, Until: t1, MergeReady: manyIDs(12)},
			want: "Window: 2026-11-21 08:00 to 2026-11-21 18:00 UTC\nTasks: 0 done, 0 created, 0 blocked\nSpend: EUR 0.00, 0 tokens\n" +
				"Merge-ready PRs: T-1, T-10, T-11, T-12, T-2, T-3, T-4, T-5, T-6, T-7 and 2 more",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Build(c.snap).Text(); got != c.want {
				t.Fatalf("text:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("T-%d", i+1)
	}
	return out
}

func TestBuildIsDeterministic(t *testing.T) {
	a := Build(fixture())
	s := fixture()
	s.Decisions[0], s.Decisions[6] = s.Decisions[6], s.Decisions[0]
	s.Risks[0], s.Risks[1] = s.Risks[1], s.Risks[0]
	s.MergeReady[0], s.MergeReady[1] = s.MergeReady[1], s.MergeReady[0]
	b := Build(s)
	if a.Text() != b.Text() || a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("input order changed the digest:\n%s\n---\n%s", a.Text(), b.Text())
	}
}

func TestBuildMasksSecretsAndShortensTitles(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	long := strings.Repeat("word ", 40)
	d := Build(Snapshot{Since: t0, Until: t1, Decisions: []Pending{
		{ID: "q:Q-1", Title: "Use key " + secret + "\nplease", Priority: 70},
		{ID: "q:Q-2", Title: long, Priority: 60},
	}})
	text := d.Text()
	if strings.Contains(text, secret) {
		t.Fatalf("secret leaked:\n%s", text)
	}
	if strings.Contains(text, "\nplease") {
		t.Fatalf("title kept its line break:\n%s", text)
	}
	for _, title := range d.Decisions.Titles {
		if n := len([]rune(title)); n > maxTitleLen {
			t.Fatalf("title has %d characters: %q", n, title)
		}
	}
}

func TestShouldSend(t *testing.T) {
	quiet := Build(Snapshot{Since: t0, Until: t1})
	pending := Build(Snapshot{Since: t0, Until: t1, Decisions: []Pending{{ID: "q:Q-1", Title: "Which?"}}})
	other := Build(Snapshot{Since: t0, Until: t1, Decisions: []Pending{{ID: "q:Q-2", Title: "Which?"}}})
	busy := Build(Snapshot{Since: t0, Until: t1, Done: 1})
	cases := []struct {
		name string
		d    Digest
		last string
		want bool
	}{
		{"fresh install, nothing happening", quiet, "", false},
		{"activity in window", busy, "", true},
		{"activity repeats unchanged state", busy, pending.Fingerprint(), true},
		{"new pending decision", pending, "", true},
		{"same pending decision, no activity", pending, pending.Fingerprint(), false},
		{"different pending decision", other, pending.Fingerprint(), true},
		{"everything cleared", quiet, pending.Fingerprint(), true},
		{"cost only", Build(Snapshot{CostEUR: 0.01}), "", true},
		{"tokens only", Build(Snapshot{Tokens: 1}), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.ShouldSend(c.last); got != c.want {
				t.Fatalf("ShouldSend = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNotification(t *testing.T) {
	d := Build(fixture())
	n := d.Notification("digest:daily:1")
	if n.Kind != "digest" || n.ID != "digest:daily:1" || n.Title != "Subspace digest: 3 done, 7 decisions pending" || n.Text != d.Text() || !n.TS.Equal(t1) {
		t.Fatalf("notification: %+v", n)
	}
}
