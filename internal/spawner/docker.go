package spawner

import "context"

// Docker is a stub; the Docker spawner is planned for Phase 2.
type Docker struct{}

// Name implements Spawner.
func (Docker) Name() string { return "docker" }

// Spawn implements Spawner.
func (Docker) Spawn(context.Context, Request) error { return ErrNotImplemented }
