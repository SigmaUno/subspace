package board

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	MaxContextBytes     = 8 << 10
	contextMessages     = 20
	contextBodyRunes    = 300
	contextReviewRunes  = 1500
	contextMinReview    = 200
	contextURLRunes     = 512
	contextPrevAssignee = 10
	contextScanPage     = 500
	reviewPrefix        = "Review (changes_requested): "
)

type ContextMessage struct {
	ID     int64     `json:"id"`
	Author string    `json:"author"`
	Role   string    `json:"role"`
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
}

type TaskContext struct {
	PreviousAssignees []string          `json:"previous_assignees"`
	Branch            string            `json:"branch"`
	PRURL             *string           `json:"pr_url"`
	Tests             *model.TestResult `json:"tests"`
	ReviewRounds      int               `json:"review_rounds"`
	ReviewComments    string            `json:"review_comments"`
	Messages          []ContextMessage  `json:"messages"`
	MinutesUsed       int               `json:"minutes_used"`
	CostUsed          float64           `json:"cost_used"`
	Truncated         bool              `json:"truncated"`
}

func (b *Board) HandoffContext(ctx context.Context, a model.Actor, t *model.Task) (*TaskContext, error) {
	if t == nil || a.ID == "" || deref(t.Assignee) != a.ID {
		return nil, nil
	}
	switch t.Status {
	case model.StatusAssigned, model.StatusChangesRequested, model.StatusInProgress:
	default:
		return nil, nil
	}
	var out *TaskContext
	err := b.read(ctx, func(tx store.Tx) error {
		prev, claims, reassigned, err := assignmentHistory(tx, t)
		if err != nil {
			return err
		}
		if t.Status == model.StatusInProgress {
			claims--
		}
		if claims <= 0 && len(prev) == 0 && !reassigned && t.ReviewRounds == 0 {
			return nil
		}
		msgs, review, err := recentThread(tx, t.ID)
		if err != nil {
			return err
		}
		if t.ReviewRounds == 0 {
			review = ""
		}
		out = &TaskContext{
			PreviousAssignees: prev,
			Branch:            t.Branch,
			Tests:             t.Tests,
			ReviewRounds:      t.ReviewRounds,
			ReviewComments:    clip(review, contextReviewRunes),
			Truncated:         len([]rune(review)) > contextReviewRunes,
			Messages:          msgs,
			MinutesUsed:       t.MinutesUsed,
			CostUsed:          t.CostUsed,
		}
		if t.PRURL != nil {
			u := clip(*t.PRURL, contextURLRunes)
			out.PRURL = &u
		}
		out.fit()
		return nil
	})
	return out, err
}

func assignmentHistory(tx store.Tx, t *model.Task) (prev []string, claims int, reassigned bool, err error) {
	evs, err := tx.ListAudit(store.AuditFilter{TaskID: t.ID, Limit: 100000})
	if err != nil {
		return nil, 0, false, err
	}
	cur := deref(t.Assignee)
	seen := map[string]bool{cur: true}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			prev = append(prev, id)
		}
	}
	for _, e := range evs {
		switch e.Tool {
		case "claim_task":
			claims++
			add(e.AgentID)
		case "reassign":
			reassigned = true
		}
		if id, ok := e.After["assignee"].(string); ok {
			add(id)
		}
	}
	if len(prev) > contextPrevAssignee {
		prev = prev[len(prev)-contextPrevAssignee:]
	}
	return nz(prev), claims, reassigned, nil
}

func recentThread(tx store.Tx, taskID string) ([]ContextMessage, string, error) {
	var ring []*model.Message
	var review string
	var since int64
	for {
		page, err := tx.ListMessages(model.ThreadTask, taskID, since, contextScanPage)
		if err != nil {
			return nil, "", err
		}
		for _, m := range page {
			ring = append(ring, m)
			if len(ring) > contextMessages {
				ring = ring[1:]
			}
			if m.AuthorRole == model.RoleReviewer && m.Kind == model.MsgNote && strings.HasPrefix(m.Body, reviewPrefix) {
				review = Redact(strings.TrimPrefix(m.Body, reviewPrefix))
			}
			since = m.ID
		}
		if len(page) < contextScanPage {
			break
		}
	}
	out := make([]ContextMessage, len(ring))
	for i, m := range ring {
		out[i] = ContextMessage{ID: m.ID, Author: m.AuthorID, Role: m.AuthorRole, Kind: m.Kind, At: m.CreatedAt,
			Text: clip(Redact(strings.Join(strings.Fields(m.Body), " ")), contextBodyRunes)}
	}
	return out, review, nil
}

func (c *TaskContext) size() int {
	raw, _ := json.Marshal(c)
	return len(raw)
}

func (c *TaskContext) fit() {
	for c.size() > MaxContextBytes {
		c.Truncated = true
		switch {
		case len(c.Messages) > 0:
			c.Messages = c.Messages[1:]
		case len([]rune(c.ReviewComments)) > contextMinReview:
			c.ReviewComments = clip(c.ReviewComments, len([]rune(c.ReviewComments))/2)
		default:
			return
		}
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
