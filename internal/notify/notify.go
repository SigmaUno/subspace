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
}

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
	}
	return "", "", nil
}

func questionEvent(ev events.Event, q *model.Question) (string, string, *Notification) {
	if q == nil {
		return "", "", nil
	}
	if ev.Type != "question.created" {
		return q.ID, ev.Type, nil
	}
	n := &Notification{ID: q.ID, Kind: "question", Title: "Question " + q.ID + " needs an answer", Priority: 4, TS: ev.TS}
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
	n := &Notification{ID: p.ID, Kind: "plan", Title: "Plan " + p.ID + " awaits approval", Priority: 4, BriefID: p.BriefID, TS: ev.TS}
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
	return t.ID, t.Status, n
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
