package spawner

import "context"

type Docker struct{}

func (Docker) Name() string { return "docker" }

func (Docker) Spawn(context.Context, Request) error { return ErrNotImplemented }
