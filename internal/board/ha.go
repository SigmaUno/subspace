package board

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func (b *Board) Reload(ctx context.Context) error {
	return b.read(ctx, func(tx store.Tx) error {
		if err := b.loadPaused(tx); err != nil {
			return err
		}
		return b.loadCapacity(tx)
	})
}

func (b *Board) syncWindow() time.Duration { return b.o.FlushEvery + b.o.SweepEvery }
