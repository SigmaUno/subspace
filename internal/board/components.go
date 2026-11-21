package board

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func (b *Board) AuditComponents(ctx context.Context, actor model.Actor, briefID string, count int) error {
	return b.write(ctx, func(tx store.Tx, w *wctx) error {
		w.emit([]string{"board", "brief:" + briefID}, "architecture.updated", map[string]any{"brief_id": briefID, "components": count})
		return w.audit(tx, actor, "set_components", "", nil, map[string]any{"brief_id": briefID, "components": count}, "")
	})
}
