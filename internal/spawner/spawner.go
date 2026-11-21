package spawner

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("spawner: not implemented")

type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusUnknown Status = "unknown"
)

type Request struct {
	AgentID  string
	Kind     string
	Model    string
	Provider string
	Repo     string
	JoinLine string
	MCPURL   string
	Token    string
	Env      map[string]string
}

type Spawner interface {
	Name() string
	Spawn(ctx context.Context, r Request) error
	Stop(ctx context.Context, agentID string) error
	Status(ctx context.Context, agentID string) (Status, error)
}
