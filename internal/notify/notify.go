package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxTitle = 120

type Notification struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Priority int       `json:"priority"`
	BriefID  string    `json:"brief_id,omitempty"`
	TaskID   string    `json:"task_id,omitempty"`
	TS       time.Time `json:"ts"`
	Actions  []Action  `json:"actions,omitempty"`
	decision string
	choices  int
}

type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Minter func(decisionID, action string, option int) (string, error)

const maxActions = 3

var Topics = []string{"questions", "plans", "control", "board"}

func describe(ev events.Event) (id, sig string, n *Notification) {
	switch d := ev.Data.(type) {
	case *model.Question:
		return questionEvent(ev, d)
	case model.Question:
		return questionEvent(ev, &d)
	case *model.Plan:
		return planEvent(ev, d)
	case model.Plan:
		return planEvent(ev, &d)
	case *model.Task:
		return taskEvent(ev, d)
	case model.Task:
		return taskEvent(ev, &d)
	}
	switch ev.Type {
	case "control.paused":
		return "control", "paused", &Notification{
			ID: "control", Kind: "paused", Title: "Subspace paused",
			Text: "All leases revoked and claims blocked", Priority: 5, TS: ev.TS,
		}
	case "control.resumed":
		return "control", "resumed", nil
	case "spend.warning":
		return spendEvent(ev)
	}
	return "", "", nil
}

func spendEvent(ev events.Event) (string, string, *Notification) {
	d, ok := ev.Data.(map[string]any)
	if !ok {
		return "", "", nil
	}
	window, _ := d["window"].(string)
	spent, _ := d["spent_eur"].(float64)
	limit, _ := d["cap_eur"].(float64)
	pct, _ := d["percent"].(float64)
	level, _ := d["level"].(string)
	id := "org_budget:" + window
	n := &Notification{
		ID: id, Kind: "org_budget", Priority: 5, TS: ev.TS,
		Title: fmt.Sprintf("Organization %s spend at %.0f%% of its cap", window, pct),
		Text:  fmt.Sprintf("€%.2f of €%.2f spent (%s)", spent, limit, level),
	}
	return id, fmt.Sprintf("%v|%v|%s", d["starts_at"], limit, level), n
}

func questionEvent(ev events.Event, q *model.Question) (string, string, *Notification) {
	if q == nil {
		return "", "", nil
	}
	if ev.Type != "question.created" {
		return q.ID, ev.Type, nil
	}
	n := &Notification{ID: q.ID, Kind: "question", Title: "Question " + q.ID + " needs an answer", Priority: 4, TS: ev.TS,
		decision: "q:" + q.ID, choices: len(q.Options)}
	if q.TaskID != nil {
		n.TaskID = *q.TaskID
	}
	if q.BriefID != nil {
		n.BriefID = *q.BriefID
	}
	n.Text = fmt.Sprintf("Agent %s asked %s%s", q.AgentID, q.ID, scope(n))
	return q.ID, ev.Type, n
}

func planEvent(ev events.Event, p *model.Plan) (string, string, *Notification) {
	if p == nil {
		return "", "", nil
	}
	if ev.Type != "plan.proposed" {
		return p.ID, ev.Type, nil
	}
	n := &Notification{ID: p.ID, Kind: "plan", Title: "Plan " + p.ID + " awaits approval", Priority: 4, BriefID: p.BriefID, TS: ev.TS,
		decision: "plan:" + p.ID}
	n.Text = fmt.Sprintf("Agent %s proposed %s%s with %d tasks", p.ProposedBy, p.ID, scope(n), len(p.Tasks))
	return p.ID, ev.Type, n
}

func taskEvent(ev events.Event, t *model.Task) (string, string, *Notification) {
	if t == nil {
		return "", "", nil
	}
	var priority int
	switch t.Status {
	case model.StatusNeedsOwner:
		priority = 5
	case model.StatusBlocked:
		priority = 4
	case model.StatusApproved:
		priority = 3
	default:
		return t.ID, t.Status, nil
	}
	n := &Notification{
		ID: t.ID, Kind: t.Status, Priority: priority, BriefID: t.BriefID, TaskID: t.ID, TS: ev.TS,
		Title: fmt.Sprintf("Task %s is %s", t.ID, strings.ReplaceAll(t.Status, "_", " ")),
	}
	n.Text = fmt.Sprintf("%s: %s", t.ID, truncate(t.Title, maxTitle))
	if t.Status == model.StatusApproved {
		n.decision = "merge:" + t.ID
	}
	return t.ID, t.Status, n
}

func withActions(n Notification, mint Minter) Notification {
	if mint == nil || n.decision == "" {
		return n
	}
	type link struct {
		label, action string
		option        int
	}
	var want []link
	switch {
	case strings.HasPrefix(n.decision, "q:"):
		if n.choices == 0 {
			return n
		}
		for i := range min(n.choices, maxActions) {
			want = append(want, link{fmt.Sprintf("Option %d", i+1), "answer", i})
		}
	case strings.HasPrefix(n.decision, "plan:"):
		want = []link{{"Approve", "approve", 0}, {"Reject", "reject", 0}}
	case strings.HasPrefix(n.decision, "merge:"):
		want = []link{{"Mark merged", "merge", 0}}
	}
	if len(want) < maxActions {
		want = append(want, link{"Snooze 1h", "skip", 0})
	}
	for _, l := range want {
		u, err := mint(n.decision, l.action, l.option)
		if err != nil {
			continue
		}
		n.Actions = append(n.Actions, Action{Label: l.label, URL: u})
	}
	return n
}

func scope(n *Notification) string {
	var parts []string
	if n.TaskID != "" {
		parts = append(parts, "task "+n.TaskID)
	}
	if n.BriefID != "" {
		parts = append(parts, "brief "+n.BriefID)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
