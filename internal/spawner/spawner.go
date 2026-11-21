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
	Image    string
	Limits   Limits
	Env      map[string]string
}

type Limits struct {
	CPUs        float64
	MemoryBytes int64
	Pids        int64
}

type Nudger interface {
	Nudge(ctx context.Context, agentID, text string) error
}

type Spawner interface {
	Name() string
	Spawn(ctx context.Context, r Request) error
	Stop(ctx context.Context, agentID string) error
	Status(ctx context.Context, agentID string) (Status, error)
}
