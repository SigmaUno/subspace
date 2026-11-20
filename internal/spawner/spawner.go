// Package spawner launches agent processes.
package spawner

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by stub spawners.
var ErrNotImplemented = errors.New("spawner: not implemented")

// Request describes an agent to launch.
type Request struct {
	AgentID  string
	Kind     string
	Model    string
	Provider string
	Repo     string
	JoinLine string
	MCPURL   string
	Token    string // in-memory only; never persisted or logged
	Env      map[string]string
}

// Spawner starts agents.
type Spawner interface {
	Name() string
	Spawn(ctx context.Context, r Request) error
}

// TODO(split B4): Noop, HC, Docker (stub) implementations + Connect snippets (connect.go).
