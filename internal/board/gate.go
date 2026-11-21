package board

import (
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type GateOp string

const (
	GateAssign  GateOp = "assign"
	GateClaim   GateOp = "claim"
	GateClaimed GateOp = "claimed"
	GateCost    GateOp = "cost"
)

type GateEvent struct {
	Op      GateOp
	AgentID string
	Task    *model.Task
	CostEUR float64
	Now     time.Time
}

type Notice struct {
	Topics []string
	Type   string
	Data   any
}

type Gate func(tx store.Tx, ev GateEvent) (*Notice, error)

func (b *Board) SetGate(g Gate) {
	b.mu.Lock()
	b.gate = g
	b.mu.Unlock()
}

func (b *Board) gated(tx store.Tx, w *wctx, op GateOp, agentID string, t *model.Task, cost float64) error {
	b.mu.Lock()
	g := b.gate
	b.mu.Unlock()
	if g == nil {
		return nil
	}
	n, err := g(unscoped(tx), GateEvent{Op: op, AgentID: agentID, Task: t, CostEUR: cost, Now: b.now()})
	if err != nil {
		return err
	}
	if n != nil {
		w.emit(n.Topics, n.Type, n.Data)
	}
	return nil
}
