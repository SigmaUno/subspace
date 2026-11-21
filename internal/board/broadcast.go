package board

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const broadcastBatch = 100

func (b *Board) PostSystemToAgents(ctx context.Context, agentIDs []string, body string) (int, error) {
	posted := 0
	for start := 0; start < len(agentIDs); start += broadcastBatch {
		batch := agentIDs[start:min(start+broadcastBatch, len(agentIDs))]
		err := b.write(ctx, func(tx store.Tx, w *wctx) error {
			for _, id := range batch {
				if err := w.post(tx, model.ThreadAgent, id, model.Owner, model.MsgSystem, body); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return posted, err
		}
		posted += len(batch)
	}
	return posted, nil
}
