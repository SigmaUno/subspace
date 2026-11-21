package client

import (
	"context"

	"github.com/SigmaUno/subspace/internal/agentout"
)

func (m *MCP) LogOutput(ctx context.Context, in agentout.Input) (*agentout.Result, error) {
	return callTool[agentout.Result](ctx, m, "log_output", in)
}
