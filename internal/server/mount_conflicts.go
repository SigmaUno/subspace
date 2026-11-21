package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const kindConflict = "conflict"

func init() {
	registerMount(func(d Deps) {
		mode, err := conflict.ParseMode(os.Getenv("SUBSPACE_CONFLICT_MODE"))
		if err != nil {
			d.Log.Warn("invalid SUBSPACE_CONFLICT_MODE, using warn", "err", err)
		}
		d.Board.SetConflictMode(mode)
		detach := decisions.Attach(d.Store, conflictHints{st: d.Store, b: d.Board})
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
	})
}

type conflictHints struct {
	st store.Store
	b  *board.Board
}

func (h conflictHints) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	if h.b.ConflictMode() == conflict.Off {
		return nil, nil
	}
	var held []*model.Task
	err := h.st.View(ctx, func(tx store.Tx) (err error) {
		held, err = tx.ListTasks(store.TaskFilter{Status: conflict.Held, Limit: 100000})
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []*decisions.Decision
	for _, t := range held {
		if t.Assignee == nil {
			continue
		}
		var earlier []string
		for _, o := range conflict.Conflicts(t, *t.Assignee, held) {
			if taskSeq(o.ID) < taskSeq(t.ID) {
				earlier = append(earlier, fmt.Sprintf("%s (%s)", o.ID, *o.Assignee))
			}
		}
		if len(earlier) == 0 {
			continue
		}
		out = append(out, &decisions.Decision{
			ID:        kindConflict + ":" + t.ID,
			Kind:      kindConflict,
			Title:     fmt.Sprintf("Path conflict: %s overlaps %s", t.ID, strings.Join(earlier, ", ")),
			Summary:   fmt.Sprintf("%s holds %s on %s; paths %s", *t.Assignee, t.ID, t.Repo, strings.Join(t.Paths, ", ")),
			BriefID:   t.BriefID,
			TaskID:    t.ID,
			AgentID:   *t.Assignee,
			Links:     decisions.Links{Thread: model.ThreadTask + ":" + t.ID},
			CreatedAt: t.UpdatedAt,
		})
	}
	return out, nil
}

func taskSeq(id string) int {
	_, n, _ := strings.Cut(id, "-")
	v, _ := strconv.Atoi(n)
	return v
}
