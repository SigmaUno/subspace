package client

import (
	"context"

	"github.com/SigmaUno/subspace/internal/agentout"
)

func (m *MCP) LogOutput(ctx context.Context, in agentout.Input) (*agentout.Result, error) {
	var out agentout.Result
	if err := m.Call(ctx, "log_output", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
