// Package board is the business-logic service: briefs, plans, tasks, agents,
// leases, questions, control, spawns. It depends only on store interfaces.
package board

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
)

// Service is the board service used by the MCP and REST layers.
// TODO(lead): the lead implements this in board/*.go; other agents code against this interface.
type Service interface {
	// Common (MCP) — all methods take the acting agent.
	GetInstructions(ctx context.Context, a model.Actor) (string, error)
	Heartbeat(ctx context.Context, a model.Actor, taskID string) (paused bool, err error)
	ClaimTask(ctx context.Context, a model.Actor, taskID string) (*model.Task, error)
	// ... remaining methods are added by the lead as implemented.
}
