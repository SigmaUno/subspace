package board

import (
	"fmt"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const blockedBriefClosed = "brief_closed"

func (b *Board) closeBrief(tx store.Tx, w *wctx, br *model.Brief) error {
	ts, err := tx.ListTasks(store.TaskFilter{BriefID: br.ID, Limit: 100000})
	if err != nil {
		return err
	}
	var cancelled, blocked int
	for _, t := range ts {
		switch t.Status {
		case model.StatusBacklog, model.StatusReady, model.StatusAssigned:
			if err := b.statusChange(tx, w, model.Owner, "close_brief", t, model.StatusCancelled, "brief closed"); err != nil {
				return err
			}
			if err := w.post(tx, model.ThreadTask, t.ID, model.Owner, model.MsgSystem, fmt.Sprintf("Brief %s was closed: task cancelled", br.ID)); err != nil {
				return err
			}
			cancelled++
		case model.StatusInProgress:
			b.endLease(w, t)
			t.BlockedReason = strp(blockedBriefClosed)
			if err := b.statusChange(tx, w, model.Owner, "close_brief", t, model.StatusBlocked, blockedBriefClosed); err != nil {
				return err
			}
			if err := w.post(tx, model.ThreadTask, t.ID, model.Owner, model.MsgSystem, fmt.Sprintf("Brief %s was closed: lease released, task blocked", br.ID)); err != nil {
				return err
			}
			blocked++
		}
	}
	cost, err := tx.BriefCost(br.ID)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, t := range ts {
		counts[t.Status]++
	}
	var parts []string
	for _, s := range model.AllStatuses {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
		}
	}
	msg := fmt.Sprintf("Brief closed: %d cancelled, %d blocked (brief_closed). Tasks: %s. Cost used: %.2f EUR.", cancelled, blocked, joinOrNone(parts), cost)
	if err := w.post(tx, model.ThreadBrief, br.ID, model.Owner, model.MsgSystem, msg); err != nil {
		return err
	}
	return w.audit(tx, model.Owner, "close_brief", "", map[string]any{"status": "open"},
		map[string]any{"status": "closed", "brief_id": br.ID, "cancelled": cancelled, "blocked": blocked, "cost_used": cost}, "")
}

func (b *Board) reopenBrief(tx store.Tx, w *wctx, br *model.Brief) error {
	msg := "Brief reopened. Tasks cancelled by closing stay cancelled; tasks blocked with brief_closed need to be unblocked."
	if err := w.post(tx, model.ThreadBrief, br.ID, model.Owner, model.MsgSystem, msg); err != nil {
		return err
	}
	return w.audit(tx, model.Owner, "reopen_brief", "", map[string]any{"status": "closed"}, map[string]any{"status": "open", "brief_id": br.ID}, "")
}

func joinOrNone(parts []string) string {
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
